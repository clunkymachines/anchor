// Package taskdispatch is the single protocol selector for outbound tasks.
package taskdispatch

import (
	"context"
	"errors"

	"anchor/internal/domain"
)

type Store interface {
	DeviceExpectedProtocol(context.Context, string, int64) (string, error)
	ListPendingDeviceTasks(context.Context, string, int64) ([]domain.DeviceTask, error)
}
type Transport interface {
	PublishDeviceTask(context.Context, domain.DeviceTask, int64) error
}

type Dispatcher struct {
	store Store
	mqtt  Transport
	coap  Transport
}

func New(store Store, mqtt Transport, coap Transport) *Dispatcher {
	return &Dispatcher{store: store, mqtt: mqtt, coap: coap}
}
func (d *Dispatcher) PublishDeviceTask(ctx context.Context, task domain.DeviceTask, organisationID int64) error {
	protocol, err := d.store.DeviceExpectedProtocol(ctx, task.DeviceID, organisationID)
	if err != nil {
		return err
	}
	switch protocol {
	case "api":
		return nil
	case "mqtt":
		if d.mqtt == nil {
			return errors.New("MQTT transport is unavailable")
		}
		return d.mqtt.PublishDeviceTask(ctx, task, organisationID)
	case "coap":
		if d.coap == nil {
			return errors.New("CoAP transport is unavailable")
		}
		return d.coap.PublishDeviceTask(ctx, task, organisationID)
	default:
		return errors.New("unsupported device protocol")
	}
}
func (d *Dispatcher) PublishPendingDeviceTasks(ctx context.Context, deviceID string, organisationID int64) error {
	protocol, err := d.store.DeviceExpectedProtocol(ctx, deviceID, organisationID)
	if err != nil {
		return err
	}
	var transport Transport
	switch protocol {
	case "api":
		return nil
	case "mqtt":
		transport = d.mqtt
	case "coap":
		transport = d.coap
	default:
		return errors.New("unsupported device protocol")
	}
	if transport == nil {
		return errors.New("transport is unavailable")
	}
	tasks, err := d.store.ListPendingDeviceTasks(ctx, deviceID, organisationID)
	if err != nil {
		return err
	}
	for _, task := range tasks {
		if err := transport.PublishDeviceTask(ctx, task, organisationID); err != nil {
			return err
		}
	}
	return nil
}
