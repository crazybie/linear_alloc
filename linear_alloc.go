//
// Copyright (C) 2020-2021 crazybie@git.com.
//
//
// Linear Allocator
//
// Improve the memory allocation and garbage collection performance.
//

package linear_ac

import (
	"fmt"
	"math"
	"reflect"
	"runtime"
	"sync/atomic"
	"unsafe"
)

///////////////////////////////////////////////////////////////////
// WARNING:
// The following structs must be matched with
// the version from the current golang runtime.
///////////////////////////////////////////////////////////////////

/// MatchWithGolangRuntime Start

type sliceHeader struct {
	Data unsafe.Pointer
	Len  int
	Cap  int
}

type stringHeader struct {
	Data unsafe.Pointer
	Len  int
}

type rtype struct {
	size       uintptr
	ptrdata    uintptr
	hash       uint32
	tflag      uint8
	align      uint8
	fieldAlign uint8
	kind       uint8
	equal      func(unsafe.Pointer, unsafe.Pointer) bool
	gcdata     *byte
	str        int32
	ptrToThis  int32
}

type emptyInterface struct {
	typ  *rtype
	data unsafe.Pointer
}

type sliceType struct {
	rtype
	elem *rtype
}

type ptrType struct {
	rtype
	elem *rtype
}

/// MatchWithGolangRuntime End

var (
	// DbgCheckPointers checks if user allocates from build-in allocator.
	DbgCheckPointers int32 = 0
)

const (
	BlockSize = 1024 * 8
	rtypeSize = unsafe.Sizeof(rtype{})
)

var (
	uintptrSize = unsafe.Sizeof(uintptr(0))

	boolType   = reflect.TypeOf(true)
	intType    = reflect.TypeOf(0)
	int32Type  = reflect.TypeOf(int32(0))
	uint32Type = reflect.TypeOf(uint32(0))
	int64Type  = reflect.TypeOf(int64(0))
	uint64Type = reflect.TypeOf(uint64(0))
	f32Type    = reflect.TypeOf(float32(0))
	f64Type    = reflect.TypeOf(float64(0))
	strType    = reflect.TypeOf("")

	reflectTypeSize = reflect.TypeOf(intType).Elem().Size()
)

type block []byte

type Allocator struct {
	enabled       bool
	staticBlock   [1024]byte
	blocks        []block
	curBlock      int
	scanObjs      []reflect.Value
	knownPointers map[uintptr]interface{}
	maps          map[unsafe.Pointer]struct{}
}

func NewLinearAc(enable bool) (ret *Allocator) {
	ret = &Allocator{
		enabled: enable,
	}
	ret.blocks = append(ret.blocks, ret.staticBlock[:0])

	if atomic.LoadInt32(&DbgCheckPointers) == 1 {
		ret.knownPointers = make(map[uintptr]interface{})
	}
	if reflectTypeSize != rtypeSize {
		panic(fmt.Errorf("golang runtime structs mismatch"))
	}
	return
}

func (ac *Allocator) Reset() {
	if !ac.enabled {
		return
	}
	if atomic.LoadInt32(&DbgCheckPointers) == 1 {
		for k := range ac.knownPointers {
			delete(ac.knownPointers, k)
		}
		ac.scanObjs = ac.scanObjs[:0]
	}

	for idx, buf := range ac.blocks {
		ac.blocks[idx] = buf[:0]
	}
	ac.curBlock = 0
	for k := range ac.maps {
		delete(ac.maps, k)
	}
}

func (ac *Allocator) New(ptrToPtr interface{}) {
	var ptrToPtrTemp interface{}
	// store in an uintptr to cheat the escape analyser
	p := *(*[2]uintptr)(unsafe.Pointer(&ptrToPtr))
	*(*[2]uintptr)(unsafe.Pointer(&ptrToPtrTemp)) = p

	if !ac.enabled {
		tp := reflect.TypeOf(ptrToPtrTemp).Elem().Elem()
		reflect.ValueOf(ptrToPtrTemp).Elem().Set(reflect.New(tp))
		return
	}

	tp := reflect.TypeOf(ptrToPtrTemp).Elem().Elem()
	v := ac.typedNew(tp)
	*(*uintptr)(unsafe.Pointer(p[1])) = (uintptr)((*emptyInterface)(unsafe.Pointer(&v)).data)
}

// New2 is slower than New due to the data copying.
// useful for migration.
func (ac *Allocator) New2(ptr interface{}) interface{} {
	var ptrTemp interface{}
	// store in an uintptr to cheat the escape analyser
	p := *(*[2]uintptr)(unsafe.Pointer(&ptr))
	*(*[2]uintptr)(unsafe.Pointer(&ptrTemp)) = p

	if !ac.enabled {
		return ptrTemp
	}

	tp := reflect.TypeOf(ptrTemp).Elem()
	ret := ac.typedNew(tp)
	copyBytes((*emptyInterface)(unsafe.Pointer(&ptrTemp)).data, (*emptyInterface)(unsafe.Pointer(&ret)).data, int(tp.Size()))

	return ret
}

func (ac *Allocator) typedNew(tp reflect.Type) (ret interface{}) {
	ptr := ac.alloc(int(tp.Size()))
	r := reflect.NewAt(tp, ptr)
	ret = r.Interface()
	if atomic.LoadInt32(&DbgCheckPointers) == 1 {
		if tp.Kind() == reflect.Struct {
			ac.scanObjs = append(ac.scanObjs, r)
		}
		ac.knownPointers[uintptr(ptr)] = ret
	}
	return
}

func (ac *Allocator) alloc(need int) unsafe.Pointer {
start:
	buf := &ac.blocks[ac.curBlock]
	used := len(*buf)
	if used+need > cap(*buf) {
		if ac.curBlock == len(ac.blocks)-1 {
			ac.blocks = append(ac.blocks, make(block, 0, int32(math.Max(float64(BlockSize), float64(need)))))
		} else if cap(ac.blocks[ac.curBlock+1]) < need {
			ac.blocks[ac.curBlock+1] = make(block, 0, need)
		}
		ac.curBlock++
		goto start
	}
	*buf = (*buf)[:used+need]
	ptr := unsafe.Pointer(&(*buf)[used])
	clearBytes(ptr, need)
	return ptr
}

func copyBytes(src, dst unsafe.Pointer, len int) {
	alignedEnd := uintptr(len) / uintptrSize * uintptrSize
	i := uintptr(0)
	for ; i < alignedEnd; i += uintptrSize {
		*(*uintptr)(unsafe.Pointer(uintptr(dst) + i)) = *(*uintptr)(unsafe.Pointer(uintptr(src) + i))
	}
	for ; i < uintptr(len); i++ {
		*(*byte)(unsafe.Pointer(uintptr(dst) + i)) = *(*byte)(unsafe.Pointer(uintptr(src) + i))
	}
}

func clearBytes(dst unsafe.Pointer, len int) {
	alignedEnd := uintptr(len) / uintptrSize * uintptrSize
	i := uintptr(0)
	for ; i < alignedEnd; i += uintptrSize {
		*(*uintptr)(unsafe.Pointer(uintptr(dst) + i)) = 0
	}
	for ; i < uintptr(len); i++ {
		*(*byte)(unsafe.Pointer(uintptr(dst) + i)) = 0
	}
}

func (ac *Allocator) NewString(v string) string {
	if !ac.enabled {
		return v
	}
	h := (*stringHeader)(unsafe.Pointer(&v))
	ptr := ac.alloc(h.Len)
	copyBytes(h.Data, ptr, h.Len)
	h.Data = ptr
	return v
}

// NewMap use build-in allocator
func (ac *Allocator) NewMap(mapPtr interface{}) {
	var mapPtrTemp interface{}
	// store in an uintptr to cheat the escape analyser
	p := *(*[2]uintptr)(unsafe.Pointer(&mapPtr))
	*(*[2]uintptr)(unsafe.Pointer(&mapPtrTemp)) = p

	if !ac.enabled {
		tp := reflect.TypeOf(mapPtrTemp).Elem()
		reflect.ValueOf(mapPtrTemp).Elem().Set(reflect.MakeMap(tp))
		return
	}

	m := reflect.MakeMap(reflect.TypeOf(mapPtrTemp).Elem())
	i := m.Interface()
	v := (*emptyInterface)(unsafe.Pointer(&i))
	reflect.ValueOf(mapPtrTemp).Elem().Set(m)

	if ac.maps == nil {
		ac.maps = make(map[unsafe.Pointer]struct{})
	}
	ac.maps[v.data] = struct{}{}

	runtime.KeepAlive(mapPtrTemp)
}

func (ac *Allocator) NewSlice(slicePtr interface{}, len, cap_ int) {
	var slicePtrTmp interface{}
	// store in an uintptr to cheat the escape analyser
	p := *(*[2]uintptr)(unsafe.Pointer(&slicePtr))
	*(*[2]uintptr)(unsafe.Pointer(&slicePtrTmp)) = p

	if !ac.enabled {
		v := reflect.MakeSlice(reflect.TypeOf(slicePtrTmp).Elem(), len, cap_)
		reflect.ValueOf(slicePtrTmp).Elem().Set(v)
		return
	}

	refSlicePtrType := reflect.TypeOf(slicePtrTmp)
	if refSlicePtrType.Kind() != reflect.Ptr || refSlicePtrType.Elem().Kind() != reflect.Slice {
		panic(fmt.Errorf("need a pointer to slice"))
	}

	if cap_ < len {
		cap_ = len
	}

	sliceEface := (*emptyInterface)(unsafe.Pointer(&slicePtrTmp))
	slice_ := (*sliceHeader)(sliceEface.data)
	ptrTyp := (*ptrType)(unsafe.Pointer(sliceEface.typ))
	sliceTyp := (*sliceType)(unsafe.Pointer(ptrTyp.elem))
	slice_.Data = ac.alloc(cap_ * int(sliceTyp.elem.size))
	slice_.Len = len
	slice_.Cap = cap_
}

// SliceAppend append pointers to slice
func (ac *Allocator) SliceAppend(slicePtr interface{}, itemPtr interface{}) {
	var slicePtrTmp interface{}
	// store in an uintptr to cheat the escape analyser
	p := *(*[2]uintptr)(unsafe.Pointer(&slicePtr))
	*(*[2]uintptr)(unsafe.Pointer(&slicePtrTmp)) = p

	if !ac.enabled {
		s := reflect.ValueOf(slicePtrTmp).Elem()
		v := reflect.Append(s, reflect.ValueOf(itemPtr))
		s.Set(v)
		return
	}

	refSlicePtrTp := reflect.TypeOf(slicePtrTmp)
	if refSlicePtrTp.Kind() != reflect.Ptr || refSlicePtrTp.Elem().Kind() != reflect.Slice {
		panic(fmt.Errorf("expect pointer to slice"))
	}
	refItemPtrTp := reflect.TypeOf(itemPtr)
	if refItemPtrTp.Kind() != reflect.Ptr {
		panic(fmt.Errorf("expect pointer as element"))
	}
	if refSlicePtrTp.Elem().Elem() != refItemPtrTp {
		panic(fmt.Errorf("elem type not match with slice"))
	}

	sliceEface := (*emptyInterface)(unsafe.Pointer(&slicePtrTmp))
	slice_ := (*sliceHeader)(sliceEface.data)
	ptrTyp := (*ptrType)(unsafe.Pointer(sliceEface.typ))
	sliceTyp := (*sliceType)(unsafe.Pointer(ptrTyp.elem))
	itemEface := (*emptyInterface)(unsafe.Pointer(&itemPtr))
	elemSz := int(sliceTyp.elem.size)
	pointerChecking := atomic.LoadInt32(&DbgCheckPointers) == 1

	if elemSz > int(unsafe.Sizeof(uintptr(0))) {
		panic(fmt.Errorf("unsupported slice"))
	}

	// grow
	if slice_.Len >= slice_.Cap {
		pre := *slice_
		slice_.Cap = slice_.Cap * 2
		if slice_.Cap == 0 {
			slice_.Cap = 1
		}
		slice_.Data = ac.alloc(slice_.Cap * elemSz)
		copyBytes(pre.Data, slice_.Data, pre.Len*elemSz)
		slice_.Len = pre.Len

		if pointerChecking {
			delete(ac.knownPointers, uintptr(pre.Data))
		}
	}

	// append
	if slice_.Len < slice_.Cap {
		d := unsafe.Pointer(uintptr(slice_.Data) + sliceTyp.elem.size*uintptr(slice_.Len))
		*(*uintptr)(d) = (uintptr)(itemEface.data)
		slice_.Len++

		if pointerChecking {
			ac.knownPointers[uintptr(slice_.Data)] = slicePtrTmp
		}
	}
}

func (ac *Allocator) CheckPointers() {
	if !ac.enabled {
		return
	}
	for _, ptr := range ac.scanObjs {
		if err := ac.checkRecursively(ptr); err != nil {
			panic(err)
		}
	}
}

func (ac *Allocator) checkRecursively(pe reflect.Value) error {
	if pe.Kind() == reflect.Ptr {
		if !pe.IsNil() {
			if _, ok := ac.knownPointers[pe.Pointer()]; !ok {
				return fmt.Errorf("unexpected external pointer: %+v", pe)
			}
			if pe.Elem().Type().Kind() == reflect.Struct {
				return ac.checkRecursively(pe.Elem())
			}
		}
		return nil
	}
	fieldName := func(i int) string {
		return fmt.Sprintf("%v.%v", pe.Type().Name(), pe.Type().Field(i).Name)
	}
	if pe.Kind() == reflect.Struct {
		for i := 0; i < pe.NumField(); i++ {
			f := pe.Field(i)
			switch f.Kind() {
			case reflect.Ptr:
				if err := ac.checkRecursively(f); err != nil {
					return fmt.Errorf("%v: %v", fieldName(i), err)
				}
			case reflect.Slice:
				if _, ok := ac.knownPointers[f.Index(0).UnsafeAddr()]; !ok {
					return fmt.Errorf("%v: unexpected external pointer: %+v", fieldName(i), f)
				}
				fallthrough
			case reflect.Array:
				for j := 0; j < f.Len(); j++ {
					if err := ac.checkRecursively(f.Index(j)); err != nil {
						return fmt.Errorf("%v: %v", fieldName(i), err)
					}
				}
			case reflect.Map:
				m := *(*unsafe.Pointer)(unsafe.Pointer(f.UnsafeAddr()))
				if _, ok := ac.maps[m]; !ok {
					return fmt.Errorf("%v: unexpected external pointer: %+v", fieldName(i), f)
				}
				for iter := f.MapRange(); iter.Next(); {
					if err := ac.checkRecursively(iter.Value()); err != nil {
						return fmt.Errorf("%v: %v", fieldName(i), err)
					}
				}
			}
		}
	}
	return nil
}

func (ac *Allocator) Bool(v bool) (r *bool) {
	if !ac.enabled {
		r = new(bool)
		*r = v
		return
	}
	r = ac.typedNew(boolType).(*bool)
	*r = v
	return
}

func (ac *Allocator) Int(v int) (r *int) {
	if !ac.enabled {
		r = new(int)
		*r = v
		return
	}
	r = ac.typedNew(intType).(*int)
	*r = v
	return
}

func (ac *Allocator) Int32(v int32) (r *int32) {
	if !ac.enabled {
		r = new(int32)
		*r = v
		return
	}

	r = ac.typedNew(int32Type).(*int32)
	*r = v
	return
}

func (ac *Allocator) Uint32(v uint32) (r *uint32) {
	if !ac.enabled {
		r = new(uint32)
		*r = v
		return
	}
	r = ac.typedNew(uint32Type).(*uint32)
	*r = v
	return
}

func (ac *Allocator) Int64(v int64) (r *int64) {
	if !ac.enabled {
		r = new(int64)
		*r = v
		return
	}
	r = ac.typedNew(int64Type).(*int64)
	*r = v
	return
}

func (ac *Allocator) Uint64(v uint64) (r *uint64) {
	if !ac.enabled {
		r = new(uint64)
		*r = v
		return r
	}
	r = ac.typedNew(uint64Type).(*uint64)
	*r = v
	return
}

func (ac *Allocator) Float32(v float32) (r *float32) {
	if !ac.enabled {
		r = new(float32)
		*r = v
		return
	}
	r = ac.typedNew(f32Type).(*float32)
	*r = v
	return
}

func (ac *Allocator) Float64(v float64) (r *float64) {
	if !ac.enabled {
		r = new(float64)
		*r = v
		return
	}
	r = ac.typedNew(f64Type).(*float64)
	*r = v
	return
}

func (ac *Allocator) String(v string) (r *string) {
	if !ac.enabled {
		r = new(string)
		*r = v
		return
	}
	r = ac.typedNew(strType).(*string)
	*r = ac.NewString(v)
	return
}

func (ac *Allocator) EnumInt32(v int32) interface{} {
	if !ac.enabled {
		r := new(int32)
		*r = v
		return r
	}
	r := ac.typedNew(int32Type).(*int32)
	*r = v
	return r
}
