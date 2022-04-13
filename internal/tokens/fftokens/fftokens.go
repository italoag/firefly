// Copyright © 2022 Kaleido, Inc.
//
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package fftokens

import (
	"context"
	"encoding/json"

	"github.com/go-resty/resty/v2"
	"github.com/hyperledger/firefly/internal/coreconfig/wsconfig"
	"github.com/hyperledger/firefly/internal/coremsgs"
	"github.com/hyperledger/firefly/pkg/blockchain"
	"github.com/hyperledger/firefly/pkg/config"
	"github.com/hyperledger/firefly/pkg/ffresty"
	"github.com/hyperledger/firefly/pkg/fftypes"
	"github.com/hyperledger/firefly/pkg/i18n"
	"github.com/hyperledger/firefly/pkg/log"
	"github.com/hyperledger/firefly/pkg/tokens"
	"github.com/hyperledger/firefly/pkg/wsclient"
)

type FFTokens struct {
	ctx            context.Context
	capabilities   *tokens.Capabilities
	callbacks      tokens.Callbacks
	configuredName string
	client         *resty.Client
	wsconn         wsclient.WSClient
}

type wsEvent struct {
	Event msgType            `json:"event"`
	ID    string             `json:"id"`
	Data  fftypes.JSONObject `json:"data"`
}

type msgType string

const (
	messageReceipt       msgType = "receipt"
	messageTokenPool     msgType = "token-pool"
	messageTokenMint     msgType = "token-mint"
	messageTokenBurn     msgType = "token-burn"
	messageTokenTransfer msgType = "token-transfer"
	messageTokenApproval msgType = "token-approval"
)

type tokenData struct {
	TX          *fftypes.UUID           `json:"tx,omitempty"`
	TXType      fftypes.TransactionType `json:"txtype,omitempty"`
	Message     *fftypes.UUID           `json:"message,omitempty"`
	MessageHash *fftypes.Bytes32        `json:"messageHash,omitempty"`
}

type createPool struct {
	Type      fftypes.TokenType  `json:"type"`
	RequestID string             `json:"requestId"`
	Signer    string             `json:"signer"`
	Data      string             `json:"data,omitempty"`
	Config    fftypes.JSONObject `json:"config"`
	Name      string             `json:"name"`
	Symbol    string             `json:"symbol"`
}

type activatePool struct {
	PoolLocator string             `json:"poolLocator"`
	Config      fftypes.JSONObject `json:"config"`
	RequestID   string             `json:"requestId,omitempty"`
}

type mintTokens struct {
	PoolLocator string `json:"poolLocator"`
	TokenIndex  string `json:"tokenIndex,omitempty"`
	To          string `json:"to"`
	Amount      string `json:"amount"`
	RequestID   string `json:"requestId,omitempty"`
	Signer      string `json:"signer"`
	Data        string `json:"data,omitempty"`
}

type burnTokens struct {
	PoolLocator string `json:"poolLocator"`
	TokenIndex  string `json:"tokenIndex,omitempty"`
	From        string `json:"from"`
	Amount      string `json:"amount"`
	RequestID   string `json:"requestId,omitempty"`
	Signer      string `json:"signer"`
	Data        string `json:"data,omitempty"`
}

type transferTokens struct {
	PoolLocator string `json:"poolLocator"`
	TokenIndex  string `json:"tokenIndex,omitempty"`
	From        string `json:"from"`
	To          string `json:"to"`
	Amount      string `json:"amount"`
	RequestID   string `json:"requestId,omitempty"`
	Signer      string `json:"signer"`
	Data        string `json:"data,omitempty"`
}

type tokenApproval struct {
	Signer      string             `json:"signer"`
	Operator    string             `json:"operator"`
	Approved    bool               `json:"approved"`
	PoolLocator string             `json:"poolLocator"`
	RequestID   string             `json:"requestId,omitempty"`
	Data        string             `json:"data,omitempty"`
	Config      fftypes.JSONObject `json:"config"`
}

func (ft *FFTokens) Name() string {
	return "fftokens"
}

func (ft *FFTokens) Init(ctx context.Context, name string, prefix config.Prefix, callbacks tokens.Callbacks) (err error) {
	ft.ctx = log.WithLogField(ctx, "proto", "fftokens")
	ft.callbacks = callbacks
	ft.configuredName = name

	if prefix.GetString(ffresty.HTTPConfigURL) == "" {
		return i18n.NewError(ctx, coremsgs.MsgMissingPluginConfig, "url", "tokens.fftokens")
	}

	ft.client = ffresty.New(ft.ctx, prefix)
	ft.capabilities = &tokens.Capabilities{}

	wsConfig := wsconfig.GenerateConfigFromPrefix(prefix)

	if wsConfig.WSKeyPath == "" {
		wsConfig.WSKeyPath = "/api/ws"
	}

	ft.wsconn, err = wsclient.New(ctx, wsConfig, nil, nil)
	if err != nil {
		return err
	}

	go ft.eventLoop()

	return nil
}

func (ft *FFTokens) Start() error {
	return ft.wsconn.Connect()
}

func (ft *FFTokens) Capabilities() *tokens.Capabilities {
	return ft.capabilities
}

func (ft *FFTokens) handleReceipt(ctx context.Context, data fftypes.JSONObject) {
	l := log.L(ctx)

	requestID := data.GetString("id")
	success := data.GetBool("success")
	message := data.GetString("message")
	transactionHash := data.GetString("transactionHash")
	if requestID == "" {
		l.Errorf("Reply cannot be processed - missing fields: %+v", data)
		return
	}
	opID, err := fftypes.ParseUUID(ctx, requestID)
	if err != nil {
		l.Errorf("Reply cannot be processed - bad ID: %+v", data)
		return
	}
	replyType := fftypes.OpStatusSucceeded
	if !success {
		replyType = fftypes.OpStatusFailed
	}
	l.Infof("Tokens '%s' reply: request=%s message=%s", replyType, requestID, message)
	ft.callbacks.TokenOpUpdate(ft, opID, replyType, transactionHash, message, data)
}

func (ft *FFTokens) handleTokenPoolCreate(ctx context.Context, data fftypes.JSONObject) (err error) {
	tokenType := data.GetString("type")
	poolLocator := data.GetString("poolLocator")
	standard := data.GetString("standard") // optional
	symbol := data.GetString("symbol")     // optional
	info := data.GetObject("info")         // optional

	// All blockchain items below are optional
	blockchainEvent := data.GetObject("blockchain")
	blockchainID := blockchainEvent.GetString("id")
	blockchainInfo := blockchainEvent.GetObject("info")
	txHash := blockchainInfo.GetString("transactionHash")
	timestampStr := blockchainEvent.GetString("timestamp")

	timestamp, err := fftypes.ParseTimeString(timestampStr)
	if err != nil {
		timestamp = fftypes.Now()
	}

	if tokenType == "" || poolLocator == "" {
		log.L(ctx).Errorf("TokenPool event is not valid - missing data: %+v", data)
		return nil // move on
	}

	// We want to process all events, even those not initiated by FireFly.
	// The "data" argument is optional, so it's important not to fail if it's missing or malformed.
	poolDataString := data.GetString("data")
	var poolData tokenData
	if err = json.Unmarshal([]byte(poolDataString), &poolData); err != nil {
		log.L(ctx).Infof("TokenPool event data could not be parsed - continuing anyway (%s): %+v", err, data)
		poolData = tokenData{}
	}

	txType := poolData.TXType
	if txType == "" {
		txType = fftypes.TransactionTypeTokenPool
	}

	pool := &tokens.TokenPool{
		Type:        fftypes.FFEnum(tokenType),
		PoolLocator: poolLocator,
		TX: fftypes.TransactionRef{
			ID:   poolData.TX,
			Type: txType,
		},
		Connector: ft.configuredName,
		Standard:  standard,
		Symbol:    symbol,
		Info:      info,
	}

	// Only include a blockchain event if there was some significant blockchain info
	if blockchainID != "" || txHash != "" {
		pool.Event = blockchain.Event{
			ProtocolID:     blockchainID,
			BlockchainTXID: txHash,
			Source:         ft.Name() + ":" + ft.configuredName,
			Name:           blockchainEvent.GetString("name"),
			Output:         blockchainEvent.GetObject("output"),
			Location:       blockchainEvent.GetString("location"),
			Signature:      blockchainEvent.GetString("signature"),
			Info:           blockchainInfo,
			Timestamp:      timestamp,
		}
	}

	// If there's an error dispatching the event, we must return the error and shutdown
	return ft.callbacks.TokenPoolCreated(ft, pool)
}

func (ft *FFTokens) handleTokenTransfer(ctx context.Context, t fftypes.TokenTransferType, data fftypes.JSONObject) (err error) {
	subject := data.GetString("subject")
	poolLocator := data.GetString("poolLocator")
	signerAddress := data.GetString("signer")
	fromAddress := data.GetString("from")
	toAddress := data.GetString("to")
	value := data.GetString("amount")
	tokenIndex := data.GetString("tokenIndex") // optional
	uri := data.GetString("uri")               // optional

	blockchainEvent := data.GetObject("blockchain")
	blockchainID := blockchainEvent.GetString("id")
	blockchainInfo := blockchainEvent.GetObject("info")
	txHash := blockchainInfo.GetString("transactionHash")  // optional
	timestampStr := blockchainEvent.GetString("timestamp") // optional

	timestamp, err := fftypes.ParseTimeString(timestampStr)
	if err != nil {
		timestamp = fftypes.Now()
	}

	if subject == "" ||
		poolLocator == "" ||
		signerAddress == "" ||
		value == "" ||
		(t != fftypes.TokenTransferTypeMint && fromAddress == "") ||
		(t != fftypes.TokenTransferTypeBurn && toAddress == "") {
		log.L(ctx).Errorf("%s event is not valid - missing data: %+v", t, data)
		return nil // move on
	}

	// We want to process all events, even those not initiated by FireFly.
	// The "data" argument is optional, so it's important not to fail if it's missing or malformed.
	transferDataString := data.GetString("data")
	var transferData tokenData
	if err = json.Unmarshal([]byte(transferDataString), &transferData); err != nil {
		log.L(ctx).Infof("%s event data could not be parsed - continuing anyway (%s): %+v", t, err, data)
		transferData = tokenData{}
	}

	var amount fftypes.FFBigInt
	_, ok := amount.Int().SetString(value, 10)
	if !ok {
		log.L(ctx).Errorf("%s event is not valid - invalid amount: %+v", t, data)
		return nil // move on
	}

	txType := transferData.TXType
	if txType == "" {
		txType = fftypes.TransactionTypeTokenTransfer
	}

	transfer := &tokens.TokenTransfer{
		PoolLocator: poolLocator,
		TokenTransfer: fftypes.TokenTransfer{
			Type:        t,
			TokenIndex:  tokenIndex,
			URI:         uri,
			Connector:   ft.configuredName,
			From:        fromAddress,
			To:          toAddress,
			Amount:      amount,
			Subject:     subject,
			Key:         signerAddress,
			Message:     transferData.Message,
			MessageHash: transferData.MessageHash,
			TX: fftypes.TransactionRef{
				ID:   transferData.TX,
				Type: txType,
			},
		},
		Event: blockchain.Event{
			ProtocolID:     blockchainID,
			BlockchainTXID: txHash,
			Source:         ft.Name() + ":" + ft.configuredName,
			Name:           blockchainEvent.GetString("name"),
			Output:         blockchainEvent.GetObject("output"),
			Location:       blockchainEvent.GetString("location"),
			Signature:      blockchainEvent.GetString("signature"),
			Info:           blockchainInfo,
			Timestamp:      timestamp,
		},
	}

	// If there's an error dispatching the event, we must return the error and shutdown
	return ft.callbacks.TokensTransferred(ft, transfer)
}

func (ft *FFTokens) handleTokenApproval(ctx context.Context, data fftypes.JSONObject) (err error) {
	subject := data.GetString("subject")
	signerAddress := data.GetString("signer")
	poolLocator := data.GetString("poolLocator")
	operatorAddress := data.GetString("operator")
	approved := data.GetBool("approved")

	blockchainEvent := data.GetObject("blockchain")
	blockchainID := blockchainEvent.GetString("id")
	blockchainInfo := blockchainEvent.GetObject("info")
	txHash := blockchainInfo.GetString("transactionHash")  // optional
	timestampStr := blockchainEvent.GetString("timestamp") // optional

	timestamp, err := fftypes.ParseTimeString(timestampStr)
	if err != nil {
		timestamp = fftypes.Now()
	}

	if subject == "" ||
		poolLocator == "" ||
		signerAddress == "" ||
		operatorAddress == "" {
		log.L(ctx).Errorf("Approval event is not valid - missing data: %+v", data)
		return nil // move on
	}

	// We want to process all events, even those not initiated by FireFly.
	// The "data" argument is optional, so it's important not to fail if it's missing or malformed.
	approvalDataString := data.GetString("data")
	var transferData tokenData
	if err = json.Unmarshal([]byte(approvalDataString), &transferData); err != nil {
		log.L(ctx).Infof("TokenApproval event data could not be parsed - continuing anyway (%s): %+v", err, data)
		transferData = tokenData{}
	}

	txType := transferData.TXType
	if txType == "" {
		txType = fftypes.TransactionTypeTokenApproval
	}

	approval := &tokens.TokenApproval{
		PoolLocator: poolLocator,
		TokenApproval: fftypes.TokenApproval{
			Connector: ft.configuredName,
			Key:       signerAddress,
			Operator:  operatorAddress,
			Approved:  approved,
			Subject:   subject,
			TX: fftypes.TransactionRef{
				ID:   transferData.TX,
				Type: txType,
			},
		},
		Event: blockchain.Event{
			ProtocolID:     blockchainID,
			BlockchainTXID: txHash,
			Source:         ft.Name() + ":" + ft.configuredName,
			Name:           blockchainEvent.GetString("name"),
			Output:         blockchainEvent.GetObject("output"),
			Location:       blockchainEvent.GetString("location"),
			Signature:      blockchainEvent.GetString("signature"),
			Info:           blockchainInfo,
			Timestamp:      timestamp,
		},
	}

	return ft.callbacks.TokensApproved(ft, approval)
}

func (ft *FFTokens) eventLoop() {
	defer ft.wsconn.Close()
	l := log.L(ft.ctx).WithField("role", "event-loop")
	ctx := log.WithLogger(ft.ctx, l)
	for {
		select {
		case <-ctx.Done():
			l.Debugf("Event loop exiting (context cancelled)")
			return
		case msgBytes, ok := <-ft.wsconn.Receive():
			if !ok {
				l.Debugf("Event loop exiting (receive channel closed)")
				return
			}

			var msg wsEvent
			err := json.Unmarshal(msgBytes, &msg)
			if err != nil {
				l.Errorf("Message cannot be parsed as JSON: %s\n%s", err, string(msgBytes))
				continue // Swallow this and move on
			}
			l.Debugf("Received %s event %s", msg.Event, msg.ID)
			switch msg.Event {
			case messageReceipt:
				ft.handleReceipt(ctx, msg.Data)
			case messageTokenPool:
				err = ft.handleTokenPoolCreate(ctx, msg.Data)
			case messageTokenMint:
				err = ft.handleTokenTransfer(ctx, fftypes.TokenTransferTypeMint, msg.Data)
			case messageTokenBurn:
				err = ft.handleTokenTransfer(ctx, fftypes.TokenTransferTypeBurn, msg.Data)
			case messageTokenTransfer:
				err = ft.handleTokenTransfer(ctx, fftypes.TokenTransferTypeTransfer, msg.Data)
			case messageTokenApproval:
				err = ft.handleTokenApproval(ctx, msg.Data)
			default:
				l.Errorf("Message unexpected: %s", msg.Event)
			}

			if err == nil && msg.Event != messageReceipt && msg.ID != "" {
				l.Debugf("Sending ack %s", msg.ID)
				ack, _ := json.Marshal(fftypes.JSONObject{
					"event": "ack",
					"data": fftypes.JSONObject{
						"id": msg.ID,
					},
				})
				err = ft.wsconn.Send(ctx, ack)
			}

			if err != nil {
				l.Errorf("Event loop exiting: %s", err)
				return
			}
		}
	}
}

func (ft *FFTokens) CreateTokenPool(ctx context.Context, opID *fftypes.UUID, pool *fftypes.TokenPool) (complete bool, err error) {
	data, _ := json.Marshal(tokenData{
		TX:     pool.TX.ID,
		TXType: pool.TX.Type,
	})
	res, err := ft.client.R().SetContext(ctx).
		SetBody(&createPool{
			Type:      pool.Type,
			RequestID: opID.String(),
			Signer:    pool.Key,
			Data:      string(data),
			Config:    pool.Config,
			Name:      pool.Name,
			Symbol:    pool.Symbol,
		}).
		Post("/api/v1/createpool")
	if err != nil || !res.IsSuccess() {
		return false, ffresty.WrapRestErr(ctx, res, err, coremsgs.MsgTokensRESTErr)
	}
	if res.StatusCode() == 200 {
		// Handle synchronous response (202 will be handled by later websocket listener)
		var obj fftypes.JSONObject
		if err := json.Unmarshal(res.Body(), &obj); err != nil {
			return false, i18n.WrapError(ctx, err, i18n.MsgJSONObjectParseFailed, res.Body())
		}
		return true, ft.handleTokenPoolCreate(ctx, obj)
	}
	return false, nil
}

func (ft *FFTokens) ActivateTokenPool(ctx context.Context, opID *fftypes.UUID, pool *fftypes.TokenPool) (complete bool, err error) {
	res, err := ft.client.R().SetContext(ctx).
		SetBody(&activatePool{
			RequestID:   opID.String(),
			PoolLocator: pool.Locator,
			Config:      pool.Config,
		}).
		Post("/api/v1/activatepool")
	if err != nil || !res.IsSuccess() {
		return false, ffresty.WrapRestErr(ctx, res, err, coremsgs.MsgTokensRESTErr)
	}
	if res.StatusCode() == 200 {
		// Handle synchronous response (202 will be handled by later websocket listener)
		var obj fftypes.JSONObject
		if err := json.Unmarshal(res.Body(), &obj); err != nil {
			return false, i18n.WrapError(ctx, err, i18n.MsgJSONObjectParseFailed, res.Body())
		}
		return true, ft.handleTokenPoolCreate(ctx, obj)
	}
	return false, nil
}

func (ft *FFTokens) MintTokens(ctx context.Context, opID *fftypes.UUID, poolLocator string, mint *fftypes.TokenTransfer) error {
	data, _ := json.Marshal(tokenData{
		TX:          mint.TX.ID,
		TXType:      mint.TX.Type,
		Message:     mint.Message,
		MessageHash: mint.MessageHash,
	})
	res, err := ft.client.R().SetContext(ctx).
		SetBody(&mintTokens{
			PoolLocator: poolLocator,
			TokenIndex:  mint.TokenIndex,
			To:          mint.To,
			Amount:      mint.Amount.Int().String(),
			RequestID:   opID.String(),
			Signer:      mint.Key,
			Data:        string(data),
		}).
		Post("/api/v1/mint")
	if err != nil || !res.IsSuccess() {
		return ffresty.WrapRestErr(ctx, res, err, coremsgs.MsgTokensRESTErr)
	}
	return nil
}

func (ft *FFTokens) BurnTokens(ctx context.Context, opID *fftypes.UUID, poolLocator string, burn *fftypes.TokenTransfer) error {
	data, _ := json.Marshal(tokenData{
		TX:          burn.TX.ID,
		TXType:      burn.TX.Type,
		Message:     burn.Message,
		MessageHash: burn.MessageHash,
	})
	res, err := ft.client.R().SetContext(ctx).
		SetBody(&burnTokens{
			PoolLocator: poolLocator,
			TokenIndex:  burn.TokenIndex,
			From:        burn.From,
			Amount:      burn.Amount.Int().String(),
			RequestID:   opID.String(),
			Signer:      burn.Key,
			Data:        string(data),
		}).
		Post("/api/v1/burn")
	if err != nil || !res.IsSuccess() {
		return ffresty.WrapRestErr(ctx, res, err, coremsgs.MsgTokensRESTErr)
	}
	return nil
}

func (ft *FFTokens) TransferTokens(ctx context.Context, opID *fftypes.UUID, poolLocator string, transfer *fftypes.TokenTransfer) error {
	data, _ := json.Marshal(tokenData{
		TX:          transfer.TX.ID,
		TXType:      transfer.TX.Type,
		Message:     transfer.Message,
		MessageHash: transfer.MessageHash,
	})
	res, err := ft.client.R().SetContext(ctx).
		SetBody(&transferTokens{
			PoolLocator: poolLocator,
			TokenIndex:  transfer.TokenIndex,
			From:        transfer.From,
			To:          transfer.To,
			Amount:      transfer.Amount.Int().String(),
			RequestID:   opID.String(),
			Signer:      transfer.Key,
			Data:        string(data),
		}).
		Post("/api/v1/transfer")
	if err != nil || !res.IsSuccess() {
		return ffresty.WrapRestErr(ctx, res, err, coremsgs.MsgTokensRESTErr)
	}
	return nil
}

func (ft *FFTokens) TokensApproval(ctx context.Context, opID *fftypes.UUID, poolLocator string, approval *fftypes.TokenApproval) error {
	data, _ := json.Marshal(tokenData{
		TX:     approval.TX.ID,
		TXType: approval.TX.Type,
	})
	res, err := ft.client.R().SetContext(ctx).
		SetBody(&tokenApproval{
			PoolLocator: poolLocator,
			Signer:      approval.Key,
			Operator:    approval.Operator,
			Approved:    approval.Approved,
			RequestID:   opID.String(),
			Data:        string(data),
			Config:      approval.Config,
		}).
		Post("/api/v1/approval")
	if err != nil || !res.IsSuccess() {
		return ffresty.WrapRestErr(ctx, res, err, coremsgs.MsgTokensRESTErr)
	}
	return nil
}
