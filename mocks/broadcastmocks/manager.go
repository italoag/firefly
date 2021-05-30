// Code generated by mockery v0.0.0-dev. DO NOT EDIT.

package broadcastmocks

import (
	context "context"

	fftypes "github.com/kaleido-io/firefly/pkg/fftypes"
	mock "github.com/stretchr/testify/mock"
)

// Manager is an autogenerated mock type for the Manager type
type Manager struct {
	mock.Mock
}

// BroadcastMessage provides a mock function with given fields: ctx, msg
func (_m *Manager) BroadcastMessage(ctx context.Context, msg *fftypes.Message) error {
	ret := _m.Called(ctx, msg)

	var r0 error
	if rf, ok := ret.Get(0).(func(context.Context, *fftypes.Message) error); ok {
		r0 = rf(ctx, msg)
	} else {
		r0 = ret.Error(0)
	}

	return r0
}

// HandleSystemBroadcast provides a mock function with given fields: ctx, msg, data
func (_m *Manager) HandleSystemBroadcast(ctx context.Context, msg *fftypes.Message, data []*fftypes.Data) (bool, error) {
	ret := _m.Called(ctx, msg, data)

	var r0 bool
	if rf, ok := ret.Get(0).(func(context.Context, *fftypes.Message, []*fftypes.Data) bool); ok {
		r0 = rf(ctx, msg, data)
	} else {
		r0 = ret.Get(0).(bool)
	}

	var r1 error
	if rf, ok := ret.Get(1).(func(context.Context, *fftypes.Message, []*fftypes.Data) error); ok {
		r1 = rf(ctx, msg, data)
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}

// Start provides a mock function with given fields:
func (_m *Manager) Start() error {
	ret := _m.Called()

	var r0 error
	if rf, ok := ret.Get(0).(func() error); ok {
		r0 = rf()
	} else {
		r0 = ret.Error(0)
	}

	return r0
}

// WaitStop provides a mock function with given fields:
func (_m *Manager) WaitStop() {
	_m.Called()
}
