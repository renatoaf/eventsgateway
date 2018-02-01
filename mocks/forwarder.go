// Automatically generated by MockGen. DO NOT EDIT!
// Source: ./forwarder/forwarder.go

package mocks

import (
	gomock "github.com/golang/mock/gomock"
)

// Mock of Forwarder interface
type MockForwarder struct {
	ctrl     *gomock.Controller
	recorder *_MockForwarderRecorder
}

// Recorder for MockForwarder (not exported)
type _MockForwarderRecorder struct {
	mock *MockForwarder
}

func NewMockForwarder(ctrl *gomock.Controller) *MockForwarder {
	mock := &MockForwarder{ctrl: ctrl}
	mock.recorder = &_MockForwarderRecorder{mock}
	return mock
}

func (_m *MockForwarder) EXPECT() *_MockForwarderRecorder {
	return _m.recorder
}

func (_m *MockForwarder) Produce(topic string, message []byte) (int32, int64, error) {
	ret := _m.ctrl.Call(_m, "Produce", topic, message)
	ret0, _ := ret[0].(int32)
	ret1, _ := ret[1].(int64)
	ret2, _ := ret[2].(error)
	return ret0, ret1, ret2
}

func (_mr *_MockForwarderRecorder) Produce(arg0, arg1 interface{}) *gomock.Call {
	return _mr.mock.ctrl.RecordCall(_mr.mock, "Produce", arg0, arg1)
}
