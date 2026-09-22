package api

import (
	"context"
	"errors"
	"log"
	"strings"

	"github.com/xtls/xray-core/app/proxyman/command"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/serial"
	"google.golang.org/protobuf/encoding/protowire"
)

const revokeUserOperationType = "xray.app.proxyman.sessionrevoke.RevokeUserOperation"

var ErrRevokeUnsupported = errors.New("xray core does not support RevokeUserOperation")

func (x *XrayHandler) AlertInbound(ctx context.Context, tag string, operation *serial.TypedMessage) error {
	client := *x.HandlerServiceClient
	_, err := client.AlterInbound(ctx, &command.AlterInboundRequest{Tag: tag, Operation: operation})
	if err != nil {
		return err
	}
	return nil
}

func (x *XrayHandler) AlertOutbound(ctx context.Context, tag string, operation *serial.TypedMessage) error {
	client := *x.HandlerServiceClient
	_, err := client.AlterOutbound(ctx, &command.AlterOutboundRequest{Tag: tag, Operation: operation})
	if err != nil {
		return err
	}
	return nil
}

func (x *XrayHandler) AddInboundUser(ctx context.Context, tag string, user Account) error {
	// Create the AddUserOperation message
	account, err := user.Message()
	if err != nil {
		return err
	}
	operation, err := ToTypedMessage(&command.AddUserOperation{
		User: &protocol.User{
			Level:   user.GetLevel(),
			Email:   user.GetEmail(),
			Account: account,
		},
	})
	if err != nil {
		return err
	}

	// Call the AlterInbound method with the AddUserOperation message
	return x.AlertInbound(ctx, tag, operation)
}

func (x *XrayHandler) RemoveInboundUser(ctx context.Context, tag, email string) error {
	operation, err := ToTypedMessage(&command.RemoveUserOperation{
		Email: email,
	})
	if err != nil {
		return err
	}

	// Call the AlterInbound method with the AddUserOperation message
	return x.AlertInbound(ctx, tag, operation)
}

func revokeUserOperation(email string) *serial.TypedMessage {
	value := protowire.AppendTag(nil, 1, protowire.BytesType)
	value = protowire.AppendString(value, email)
	return &serial.TypedMessage{Type: revokeUserOperationType, Value: value}
}

func (x *XrayHandler) RevokeInboundUser(ctx context.Context, tag, email string) error {
	if x.revokeUnsupported.Load() {
		return ErrRevokeUnsupported
	}

	err := x.AlertInbound(ctx, tag, revokeUserOperation(email))
	if err != nil && strings.Contains(err.Error(), "unknown operation") {
		if x.revokeUnsupported.CompareAndSwap(false, true) {
			log.Println("xray core does not support RevokeUserOperation; removed users keep their open connections until those close")
		}
		return ErrRevokeUnsupported
	}
	return err
}

func (x *XrayHandler) AddOutboundUser(ctx context.Context, tag string, user Account) error {
	// Create the AddUserOperation message
	account, err := user.Message()
	if err != nil {
		return err
	}
	operation, err := ToTypedMessage(&command.AddUserOperation{
		User: &protocol.User{
			Level:   user.GetLevel(),
			Email:   user.GetEmail(),
			Account: account,
		},
	})
	if err != nil {
		return err
	}

	// Call the AlterInbound method with the AddUserOperation message
	return x.AlertOutbound(ctx, tag, operation)
}

func (x *XrayHandler) RemoveOutboundUser(ctx context.Context, tag, email string) error {
	operation, err := ToTypedMessage(&command.RemoveUserOperation{
		Email: email,
	})
	if err != nil {
		return err
	}

	// Call the AlterInbound method with the AddUserOperation message
	return x.AlertOutbound(ctx, tag, operation)
}
