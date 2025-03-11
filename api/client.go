package api

import (
	"bytes"
	"encoding/hex"
	"fmt"

	"go.sia.tech/core/types"
	"go.sia.tech/jape"
)

// A Client provides methods for interacting with a minerd API server.
type Client struct {
	c jape.Client
}


// MiningGetBlockTemplate returns a block template for mining.
func (c *Client) MiningGetBlockTemplate(payoutAddr types.Address, longPollID string) (resp MiningGetBlockTemplateResponse, err error) {
	err = c.c.POST("/mining/getblocktemplate", MiningGetBlockTemplateRequest{
		LongPollID:    longPollID,
		PayoutAddress: payoutAddr,
	}, &resp)
	return
}

// MiningSubmitBlock submits a mined block to the network.
func (c *Client) MiningSubmitBlock(b types.Block) error {
	buf := new(bytes.Buffer)
	enc := types.NewEncoder(buf)
	if b.V2 == nil {
		types.V1Block(b).EncodeTo(enc)
	} else {
		types.V2Block(b).EncodeTo(enc)
	}
	if err := enc.Flush(); err != nil {
		return fmt.Errorf("failed to encode block: %w", err)
	}
	return c.c.POST("/mining/submitblock", MiningSubmitBlockRequest{
		Params: []string{hex.EncodeToString(buf.Bytes())},
	}, nil)
}


// NewClient returns a client that communicates with a walletd server listening
// on the specified address.
func NewClient(addr, password string) *Client {
	return &Client{c: jape.Client{
		BaseURL:  addr,
		Password: password,
	}}
}
