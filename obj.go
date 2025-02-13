// Copyright 2017 Tamás Gulácsi
//
//
//    Licensed under the Apache License, Version 2.0 (the "License");
//    you may not use this file except in compliance with the License.
//    You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
//    Unless required by applicable law or agreed to in writing, software
//    distributed under the License is distributed on an "AS IS" BASIS,
//    WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
//    See the License for the specific language governing permissions and
//    limitations under the License.

package goracle

/*
#include <stdlib.h>
#include "dpiImpl.h"
*/
import "C"
import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"unsafe"

	errors "golang.org/x/xerrors"
)

var _ = fmt.Printf

// Object represents a dpiObject.
type Object struct {
	ObjectType
	scratch   Data
	dpiObject *C.dpiObject
}

func (O *Object) getError() error { return O.conn.getError() }

// ErrNoSuchKey is the error for missing key in lookup.
var ErrNoSuchKey = errors.New("no such key")

// GetAttribute gets the i-th attribute into data.
func (O *Object) GetAttribute(data *Data, name string) error {
	if O == nil || O.dpiObject == nil {
		panic("nil dpiObject")
	}
	attr, ok := O.Attributes[name]
	if !ok {
		return errors.Errorf("%s: %w", name, ErrNoSuchKey)
	}

	data.reset()
	if data.dpiData == nil {
		data.dpiData = &C.dpiData{isNull: 0}
	}
	data.NativeTypeNum = attr.NativeTypeNum
	data.ObjectType = attr.ObjectType
	// the maximum length of that buffer must be supplied
	// in the value.asBytes.length attribute before calling this function.
	if attr.NativeTypeNum == C.DPI_NATIVE_TYPE_BYTES && attr.OracleTypeNum == C.DPI_ORACLE_TYPE_NUMBER {
		var a [39]byte
		C.dpiData_setBytes(data.dpiData, (*C.char)(unsafe.Pointer(&a[0])), C.uint32_t(len(a)))
	}

	//fmt.Printf("getAttributeValue(%p, %p, %d, %+v)\n", O.dpiObject, attr.dpiObjectAttr, data.NativeTypeNum, data.dpiData)
	if C.dpiObject_getAttributeValue(O.dpiObject, attr.dpiObjectAttr, data.NativeTypeNum, data.dpiData) == C.DPI_FAILURE {
		return errors.Errorf("getAttributeValue(%q, obj=%+v, attr=%+v, typ=%d): %w", name, O, attr.dpiObjectAttr, data.NativeTypeNum, O.getError())
	}
	//fmt.Printf("getAttributeValue(%p, %q=%p, %d, %+v)\n", O.dpiObject, attr.Name, attr.dpiObjectAttr, data.NativeTypeNum, data.dpiData)
	return nil
}

// SetAttribute sets the named attribute with data.
func (O *Object) SetAttribute(name string, data *Data) error {
	if !strings.Contains(name, `"`) {
		name = strings.ToUpper(name)
	}
	attr := O.Attributes[name]
	if data.NativeTypeNum == 0 {
		data.NativeTypeNum = attr.NativeTypeNum
		data.ObjectType = attr.ObjectType
	}
	if C.dpiObject_setAttributeValue(O.dpiObject, attr.dpiObjectAttr, data.NativeTypeNum, data.dpiData) == C.DPI_FAILURE {
		return O.getError()
	}
	return nil
}

// Set is a convenience function to set the named attribute with the given value.
func (O *Object) Set(name string, v interface{}) error {
	if data, ok := v.(*Data); ok {
		return O.SetAttribute(name, data)
	}
	if err := O.scratch.Set(v); err != nil {
		return err
	}
	return O.SetAttribute(name, &O.scratch)
}

// ResetAttributes prepare all attributes for use the object as IN parameter
func (O *Object) ResetAttributes() error {
	var data Data
	for _, attr := range O.Attributes {
		data.reset()
		data.NativeTypeNum = attr.NativeTypeNum
		data.ObjectType = attr.ObjectType
		if attr.NativeTypeNum == C.DPI_NATIVE_TYPE_BYTES && attr.OracleTypeNum == C.DPI_ORACLE_TYPE_NUMBER {
			a := make([]byte, attr.Precision)
			C.dpiData_setBytes(data.dpiData, (*C.char)(unsafe.Pointer(&a[0])), C.uint32_t(attr.Precision))
		}
		if C.dpiObject_setAttributeValue(O.dpiObject, attr.dpiObjectAttr, data.NativeTypeNum, data.dpiData) == C.DPI_FAILURE {
			return O.getError()
		}
	}

	return nil
}

// Get scans the named attribute into dest, and returns it.
func (O *Object) Get(name string) (interface{}, error) {
	if err := O.GetAttribute(&O.scratch, name); err != nil {
		return nil, err
	}
	isObject := O.scratch.IsObject()
	if isObject {
		O.scratch.ObjectType = O.Attributes[name].ObjectType
	}
	v := O.scratch.Get()
	if !isObject {
		return v, nil
	}
	sub := v.(*Object)
	if sub != nil && sub.CollectionOf != nil {
		return &ObjectCollection{Object: sub}, nil
	}
	return sub, nil
}

// ObjectRef implements userType interface.
func (O *Object) ObjectRef() *Object {
	return O
}

// Collection returns &ObjectCollection{Object: O} iff the Object is a collection.
// Otherwise it returns nil.
func (O *Object) Collection() *ObjectCollection {
	if O.ObjectType.CollectionOf == nil {
		return nil
	}
	return &ObjectCollection{Object: O}
}

// Close releases a reference to the object.
func (O *Object) Close() error {
	if O.dpiObject == nil {
		return nil
	}
	if rc := C.dpiObject_release(O.dpiObject); rc == C.DPI_FAILURE {
		return errors.Errorf("error on close object: %w", O.getError())
	}
	O.dpiObject = nil

	return nil
}

// ObjectCollection represents a Collection of Objects - itself an Object, too.
type ObjectCollection struct {
	*Object
}

// ErrNotCollection is returned when the Object is not a collection.
var ErrNotCollection = errors.New("not collection")

// ErrNotExist is returned when the collection's requested element does not exist.
var ErrNotExist = errors.New("not exist")

// AsSlice retrieves the collection into a slice.
func (O *ObjectCollection) AsSlice(dest interface{}) (interface{}, error) {
	var dr reflect.Value
	needsInit := dest == nil
	if !needsInit {
		dr = reflect.ValueOf(dest)
	}
	for i, err := O.First(); err == nil; i, err = O.Next(i) {
		if O.CollectionOf.NativeTypeNum == C.DPI_NATIVE_TYPE_OBJECT {
			O.scratch.ObjectType = *O.CollectionOf
		}
		if err = O.GetItem(&O.scratch, i); err != nil {
			return dest, err
		}
		vr := reflect.ValueOf(O.scratch.Get())
		if needsInit {
			needsInit = false
			length, lengthErr := O.Len()
			if lengthErr != nil {
				return dr.Interface(), lengthErr
			}
			dr = reflect.MakeSlice(reflect.SliceOf(vr.Type()), 0, length)
		}
		dr = reflect.Append(dr, vr)
	}
	return dr.Interface(), nil
}

// AppendData to the collection.
func (O *ObjectCollection) AppendData(data *Data) error {
	if C.dpiObject_appendElement(O.dpiObject, data.NativeTypeNum, data.dpiData) == C.DPI_FAILURE {
		return errors.Errorf("append(%d): %w", data.NativeTypeNum, O.getError())
	}
	return nil
}

// Append v to the collection.
func (O *ObjectCollection) Append(v interface{}) error {
	if data, ok := v.(*Data); ok {
		return O.AppendData(data)
	}
	if err := O.scratch.Set(v); err != nil {
		return err
	}
	return O.AppendData(&O.scratch)
}

// AppendObject adds an Object to the collection.
func (O *ObjectCollection) AppendObject(obj *Object) error {
	O.scratch = Data{
		ObjectType:    obj.ObjectType,
		NativeTypeNum: C.DPI_NATIVE_TYPE_OBJECT,
		dpiData:       &C.dpiData{isNull: 1},
	}
	O.scratch.SetObject(obj)
	return O.Append(&O.scratch)
}

// Delete i-th element of the collection.
func (O *ObjectCollection) Delete(i int) error {
	if C.dpiObject_deleteElementByIndex(O.dpiObject, C.int32_t(i)) == C.DPI_FAILURE {
		return errors.Errorf("delete(%d): %w", i, O.getError())
	}
	return nil
}

// GetItem gets the i-th element of the collection into data.
func (O *ObjectCollection) GetItem(data *Data, i int) error {
	if data == nil {
		panic("data cannot be nil")
	}
	idx := C.int32_t(i)
	var exists C.int
	if C.dpiObject_getElementExistsByIndex(O.dpiObject, idx, &exists) == C.DPI_FAILURE {
		return errors.Errorf("exists(%d): %w", idx, O.getError())
	}
	if exists == 0 {
		return ErrNotExist
	}
	data.reset()
	data.NativeTypeNum = O.CollectionOf.NativeTypeNum
	data.ObjectType = *O.CollectionOf
	if C.dpiObject_getElementValueByIndex(O.dpiObject, idx, data.NativeTypeNum, data.dpiData) == C.DPI_FAILURE {
		return errors.Errorf("get(%d[%d]): %w", idx, data.NativeTypeNum, O.getError())
	}
	return nil
}

// Get the i-th element of the collection.
func (O *ObjectCollection) Get(i int) (interface{}, error) {
	var data Data
	err := O.GetItem(&data, i)
	return data.Get(), err
}

// SetItem sets the i-th element of the collection with data.
func (O *ObjectCollection) SetItem(i int, data *Data) error {
	if C.dpiObject_setElementValueByIndex(O.dpiObject, C.int32_t(i), data.NativeTypeNum, data.dpiData) == C.DPI_FAILURE {
		return errors.Errorf("set(%d[%d]): %w", i, data.NativeTypeNum, O.getError())
	}
	return nil
}

// Set the i-th element of the collection with value.
func (O *ObjectCollection) Set(i int, v interface{}) error {
	if data, ok := v.(*Data); ok {
		return O.SetItem(i, data)
	}
	if err := O.scratch.Set(v); err != nil {
		return err
	}
	return O.SetItem(i, &O.scratch)
}

// First returns the first element's index of the collection.
func (O *ObjectCollection) First() (int, error) {
	var exists C.int
	var idx C.int32_t
	if C.dpiObject_getFirstIndex(O.dpiObject, &idx, &exists) == C.DPI_FAILURE {
		return 0, errors.Errorf("first: %w", O.getError())
	}
	if exists == 1 {
		return int(idx), nil
	}
	return 0, ErrNotExist
}

// Last returns the index of the last element.
func (O *ObjectCollection) Last() (int, error) {
	var exists C.int
	var idx C.int32_t
	if C.dpiObject_getLastIndex(O.dpiObject, &idx, &exists) == C.DPI_FAILURE {
		return 0, errors.Errorf("last: %w", O.getError())
	}
	if exists == 1 {
		return int(idx), nil
	}
	return 0, ErrNotExist
}

// Next returns the succeeding index of i.
func (O *ObjectCollection) Next(i int) (int, error) {
	var exists C.int
	var idx C.int32_t
	if C.dpiObject_getNextIndex(O.dpiObject, C.int32_t(i), &idx, &exists) == C.DPI_FAILURE {
		return 0, errors.Errorf("next(%d): %w", i, O.getError())
	}
	if exists == 1 {
		return int(idx), nil
	}
	return 0, ErrNotExist
}

// Len returns the length of the collection.
func (O *ObjectCollection) Len() (int, error) {
	var size C.int32_t
	if C.dpiObject_getSize(O.dpiObject, &size) == C.DPI_FAILURE {
		return 0, errors.Errorf("len: %w", O.getError())
	}
	return int(size), nil
}

// Trim the collection to n.
func (O *ObjectCollection) Trim(n int) error {
	if C.dpiObject_trim(O.dpiObject, C.uint32_t(n)) == C.DPI_FAILURE {
		return O.getError()
	}
	return nil
}

// ObjectType holds type info of an Object.
type ObjectType struct {
	dpiObjectType *C.dpiObjectType
	conn          *conn

	Schema, Name                        string
	DBSize, ClientSizeInBytes, CharSize int
	CollectionOf                        *ObjectType
	Attributes                          map[string]ObjectAttribute
	OracleTypeNum                       C.dpiOracleTypeNum
	NativeTypeNum                       C.dpiNativeTypeNum
	Precision                           int16
	Scale                               int8
	FsPrecision                         uint8
}

func (t ObjectType) getError() error { return t.conn.getError() }

func (t ObjectType) String() string {
	if t.Schema == "" {
		return t.Name
	}
	return t.Schema + "." + t.Name
}

// FullName returns the object's name with the schame prepended.
func (t ObjectType) FullName() string {
	if t.Schema == "" {
		return t.Name
	}
	return t.Schema + "." + t.Name
}

// GetObjectType returns the ObjectType of a name.
//
// The name is uppercased! Because here Oracle seems to be case-sensitive.
// To leave it as is, enclose it in "-s!
func (c *conn) GetObjectType(name string) (ObjectType, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !strings.Contains(name, "\"") {
		name = strings.ToUpper(name)
	}
	if o, ok := c.objTypes[name]; ok {
		return o, nil
	}
	cName := C.CString(name)
	defer func() { C.free(unsafe.Pointer(cName)) }()
	objType := (*C.dpiObjectType)(C.malloc(C.sizeof_void))
	if C.dpiConn_getObjectType(c.dpiConn, cName, C.uint32_t(len(name)), &objType) == C.DPI_FAILURE {
		C.free(unsafe.Pointer(objType))
		return ObjectType{}, errors.Errorf("getObjectType(%q) conn=%p: %w", name, c.dpiConn, c.getError())
	}
	t := ObjectType{conn: c, dpiObjectType: objType}
	err := t.init()
	if err == nil {
		if c.objTypes == nil {
			c.objTypes = make(map[string]ObjectType)
		}
		c.objTypes[name] = t
		c.objTypes[t.FullName()] = t
	}
	return t, err
}

// NewObject returns a new Object with ObjectType type.
//
// As with all Objects, you MUST call Close on it when not needed anymore!
func (t ObjectType) NewObject() (*Object, error) {
	obj := (*C.dpiObject)(C.malloc(C.sizeof_void))
	if C.dpiObjectType_createObject(t.dpiObjectType, &obj) == C.DPI_FAILURE {
		C.free(unsafe.Pointer(obj))
		return nil, t.getError()
	}
	O := &Object{ObjectType: t, dpiObject: obj}
	// https://github.com/oracle/odpi/issues/112#issuecomment-524479532
	return O, O.ResetAttributes()
}

// NewCollection returns a new Collection object with ObjectType type.
// If the ObjectType is not a Collection, it returns ErrNotCollection error.
func (t ObjectType) NewCollection() (*ObjectCollection, error) {
	if t.CollectionOf == nil {
		return nil, ErrNotCollection
	}
	O, err := t.NewObject()
	if err != nil {
		return nil, err
	}
	return &ObjectCollection{Object: O}, nil
}

// Close releases a reference to the object type.
func (t *ObjectType) close() error {
	if t == nil {
		return nil
	}
	for _, attr := range t.Attributes {
		err := attr.Close()
		if err != nil {
			return err
		}
	}

	t.Attributes = nil
	d := t.dpiObjectType
	t.dpiObjectType = nil
	if d == nil {
		return nil
	}

	if rc := C.dpiObjectType_release(d); rc == C.DPI_FAILURE {
		return errors.Errorf("error on close object type: %w", t.getError())
	}

	return nil
}

func wrapObject(c *conn, objectType *C.dpiObjectType, object *C.dpiObject) (*Object, error) {
	if objectType == nil {
		return nil, errors.New("objectType is nil")
	}
	if C.dpiObject_addRef(object) == C.DPI_FAILURE {
		return nil, c.getError()
	}
	o := &Object{
		ObjectType: ObjectType{dpiObjectType: objectType, conn: c},
		dpiObject:  object,
	}
	return o, o.init()
}

func (t *ObjectType) init() error {
	if t.conn == nil {
		panic("conn is nil")
	}
	if t.Name != "" && t.Attributes != nil {
		return nil
	}
	if t.dpiObjectType == nil {
		return nil
	}
	var info C.dpiObjectTypeInfo
	if C.dpiObjectType_getInfo(t.dpiObjectType, &info) == C.DPI_FAILURE {
		return errors.Errorf("%v.getInfo: %w", t, t.getError())
	}
	t.Schema = C.GoStringN(info.schema, C.int(info.schemaLength))
	t.Name = C.GoStringN(info.name, C.int(info.nameLength))
	t.CollectionOf = nil
	numAttributes := int(info.numAttributes)

	if info.isCollection == 1 {
		t.CollectionOf = &ObjectType{conn: t.conn}
		if err := t.CollectionOf.fromDataTypeInfo(info.elementTypeInfo); err != nil {
			return err
		}
		if t.CollectionOf.Name == "" {
			t.CollectionOf.Schema = t.Schema
			t.CollectionOf.Name = t.Name
		}
	}
	if numAttributes == 0 {
		t.Attributes = map[string]ObjectAttribute{}
		return nil
	}
	t.Attributes = make(map[string]ObjectAttribute, numAttributes)
	attrs := make([]*C.dpiObjectAttr, numAttributes)
	if C.dpiObjectType_getAttributes(t.dpiObjectType,
		C.uint16_t(len(attrs)),
		(**C.dpiObjectAttr)(unsafe.Pointer(&attrs[0])),
	) == C.DPI_FAILURE {
		return errors.Errorf("%v.getAttributes: %w", t, t.getError())
	}
	for i, attr := range attrs {
		var attrInfo C.dpiObjectAttrInfo
		if C.dpiObjectAttr_getInfo(attr, &attrInfo) == C.DPI_FAILURE {
			return errors.Errorf("%v.attr_getInfo: %w", attr, t.getError())
		}
		if Log != nil {
			Log("i", i, "attrInfo", attrInfo)
		}
		typ := attrInfo.typeInfo
		sub, err := objectTypeFromDataTypeInfo(t.conn, typ)
		if err != nil {
			return err
		}
		objAttr := ObjectAttribute{
			dpiObjectAttr: attr,
			Name:          C.GoStringN(attrInfo.name, C.int(attrInfo.nameLength)),
			ObjectType:    sub,
		}
		//fmt.Printf("%d=%q. typ=%+v sub=%+v\n", i, objAttr.Name, typ, sub)
		t.Attributes[objAttr.Name] = objAttr
	}
	return nil
}

func (t *ObjectType) fromDataTypeInfo(typ C.dpiDataTypeInfo) error {
	t.dpiObjectType = typ.objectType

	t.OracleTypeNum = typ.oracleTypeNum
	t.NativeTypeNum = typ.defaultNativeTypeNum
	t.DBSize = int(typ.dbSizeInBytes)
	t.ClientSizeInBytes = int(typ.clientSizeInBytes)
	t.CharSize = int(typ.sizeInChars)
	t.Precision = int16(typ.precision)
	t.Scale = int8(typ.scale)
	t.FsPrecision = uint8(typ.fsPrecision)
	return t.init()
}
func objectTypeFromDataTypeInfo(conn *conn, typ C.dpiDataTypeInfo) (ObjectType, error) {
	if conn == nil {
		panic("conn is nil")
	}
	if typ.oracleTypeNum == 0 {
		panic("typ is nil")
	}
	t := ObjectType{conn: conn}
	err := t.fromDataTypeInfo(typ)
	return t, err
}

// ObjectAttribute is an attribute of an Object.
type ObjectAttribute struct {
	Name          string
	dpiObjectAttr *C.dpiObjectAttr
	ObjectType
}

// Close the ObjectAttribute.
func (A ObjectAttribute) Close() error {
	attr := A.dpiObjectAttr
	if attr == nil {
		return nil
	}

	A.dpiObjectAttr = nil
	if C.dpiObjectAttr_release(attr) == C.DPI_FAILURE {
		return A.getError()
	}
	return nil
}

// GetObjectType returns the ObjectType for the name.
func GetObjectType(ctx context.Context, ex Execer, typeName string) (ObjectType, error) {
	c, err := getConn(ctx, ex)
	if err != nil {
		return ObjectType{}, errors.Errorf("getConn for %s: %w", typeName, err)
	}
	return c.GetObjectType(typeName)
}
