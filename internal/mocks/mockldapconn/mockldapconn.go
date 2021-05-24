// Copyright 2020-2021 the Pinniped contributors. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0
//

// Code generated by MockGen. DO NOT EDIT.
// Source: go.pinniped.dev/internal/upstreamldap (interfaces: Conn)

// Package mockldapconn is a generated GoMock package.
package mockldapconn

import (
	reflect "reflect"

	ldap "github.com/go-ldap/ldap/v3"
	gomock "github.com/golang/mock/gomock"
)

// MockConn is a mock of Conn interface.
type MockConn struct {
	ctrl     *gomock.Controller
	recorder *MockConnMockRecorder
}

// MockConnMockRecorder is the mock recorder for MockConn.
type MockConnMockRecorder struct {
	mock *MockConn
}

// NewMockConn creates a new mock instance.
func NewMockConn(ctrl *gomock.Controller) *MockConn {
	mock := &MockConn{ctrl: ctrl}
	mock.recorder = &MockConnMockRecorder{mock}
	return mock
}

// EXPECT returns an object that allows the caller to indicate expected use.
func (m *MockConn) EXPECT() *MockConnMockRecorder {
	return m.recorder
}

// Bind mocks base method.
func (m *MockConn) Bind(arg0, arg1 string) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Bind", arg0, arg1)
	ret0, _ := ret[0].(error)
	return ret0
}

// Bind indicates an expected call of Bind.
func (mr *MockConnMockRecorder) Bind(arg0, arg1 interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Bind", reflect.TypeOf((*MockConn)(nil).Bind), arg0, arg1)
}

// Close mocks base method.
func (m *MockConn) Close() {
	m.ctrl.T.Helper()
	m.ctrl.Call(m, "Close")
}

// Close indicates an expected call of Close.
func (mr *MockConnMockRecorder) Close() *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Close", reflect.TypeOf((*MockConn)(nil).Close))
}

// Search mocks base method.
func (m *MockConn) Search(arg0 *ldap.SearchRequest) (*ldap.SearchResult, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Search", arg0)
	ret0, _ := ret[0].(*ldap.SearchResult)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// Search indicates an expected call of Search.
func (mr *MockConnMockRecorder) Search(arg0 interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Search", reflect.TypeOf((*MockConn)(nil).Search), arg0)
}

// SearchWithPaging mocks base method.
func (m *MockConn) SearchWithPaging(arg0 *ldap.SearchRequest, arg1 uint32) (*ldap.SearchResult, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "SearchWithPaging", arg0, arg1)
	ret0, _ := ret[0].(*ldap.SearchResult)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// SearchWithPaging indicates an expected call of SearchWithPaging.
func (mr *MockConnMockRecorder) SearchWithPaging(arg0, arg1 interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "SearchWithPaging", reflect.TypeOf((*MockConn)(nil).SearchWithPaging), arg0, arg1)
}
