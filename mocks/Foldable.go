// Code generated by mockery v1.0.0. DO NOT EDIT.

package mocks

import mock "github.com/stretchr/testify/mock"
import sortedset "gitlab.dusk.network/dusk-core/dusk-go/pkg/util/nativeutils/sortedset"

// Foldable is an autogenerated mock type for the Foldable type
type Foldable struct {
	mock.Mock
}

// IsMember provides a mock function with given fields: _a0, _a1, _a2
func (_m *Foldable) IsMember(_a0 []byte, _a1 uint64, _a2 uint8) bool {
	ret := _m.Called(_a0, _a1, _a2)

	var r0 bool
	if rf, ok := ret.Get(0).(func([]byte, uint64, uint8) bool); ok {
		r0 = rf(_a0, _a1, _a2)
	} else {
		r0 = ret.Get(0).(bool)
	}

	return r0
}

// Pack provides a mock function with given fields: _a0, _a1, _a2
func (_m *Foldable) Pack(_a0 sortedset.Set, _a1 uint64, _a2 uint8) uint64 {
	ret := _m.Called(_a0, _a1, _a2)

	var r0 uint64
	if rf, ok := ret.Get(0).(func(sortedset.Set, uint64, uint8) uint64); ok {
		r0 = rf(_a0, _a1, _a2)
	} else {
		r0 = ret.Get(0).(uint64)
	}

	return r0
}

// Quorum provides a mock function with given fields:
func (_m *Foldable) Quorum() int {
	ret := _m.Called()

	var r0 int
	if rf, ok := ret.Get(0).(func() int); ok {
		r0 = rf()
	} else {
		r0 = ret.Get(0).(int)
	}

	return r0
}

// Unpack provides a mock function with given fields: _a0, _a1, _a2
func (_m *Foldable) Unpack(_a0 uint64, _a1 uint64, _a2 uint8) sortedset.Set {
	ret := _m.Called(_a0, _a1, _a2)

	var r0 sortedset.Set
	if rf, ok := ret.Get(0).(func(uint64, uint64, uint8) sortedset.Set); ok {
		r0 = rf(_a0, _a1, _a2)
	} else {
		if ret.Get(0) != nil {
			r0 = ret.Get(0).(sortedset.Set)
		}
	}

	return r0
}
