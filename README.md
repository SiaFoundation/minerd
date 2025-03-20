# minerd

`minerd` is a specialized daemon that wraps the functionality of
[walletd](https://github.com/SiaFoundation/walletd) and adds additional
functionality useful for miners and mining pools. It also serves the same UI as
`walletd` and the same API endpoints.

## Configuration

The configuration for `minerd` is mostly the same as `walletd`. The names of the
environment variables were changed from starting with the prefix `WALLETD_` to
`MINERD_`.

If the getblocktemplate endpoint is used, the payout address needs to be
configured. This can be done using the:
- `mining.payoutAddress` CLI flag
- `MINERD_PAYOUT_ADDRESS` environment variable
- `payoutAddress` field in the `minerd.yml` file under the `mining` section

Check out the [walletd repo](https://github.com/SiaFoundation/walletd) for more
information on how to configure `walletd` related settings.

## API

`minerd` serves all `walletd` API endpoints under `/api` just like `walletd`.
These are documented [here](https://api.sia.tech/walletd).

Additionally, `minerd` serves the following endpoints:

### `POST /api/miner/getblocktemplate`

This endpoint can be used to obtain a block template for mining similar to BIP22 templates.
The version is either 1 or 2 depending on whether the block is a a V1 or V2 block.

The `txType` field of transactions is also either 1 or 2 depending on whether
the transaction is a V1 or V2 transaction.

***Example Request***:
```json
{
  "longpollid" : "",
}
```

***Example Response***:
```json
{
  "transactions": [
   {
    "data": "01000000000000001cf5f9f076dcf1749501dab9d891253a66fb3982066fd533ee97d4e7545200c700000000000000000100000000000000656432353531390000000000000000002000000000000000f2d4cfa8a5e3f77fd12674c942f849a85fbd5eb763327ee5164bd57768c82b95010000000000000002000000000000000b0000000000000052b7d2dcc80cd2e4000000cc58d6750fd0b836fc90c7a166c670749725dd9ebc54fe8d14879b303fb22cb10d0000000000000003c90772fc37cca71797800000cc58d6750fd0b836fc90c7a166c670749725dd9ebc54fe8d14879b303fb22cb10000000000000000000000000000000000000000000000000000000000000000000000000000000001000000000000000a00000000000000043c33c1937564800000000000000000000001000000000000001cf5f9f076dcf1749501dab9d891253a66fb3982066fd533ee97d4e7545200c7000000000000000000000000000000000100000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000004000000000000000af9d6897d047fe35db71969187d0d5ecfac50e09f7a105b191f50d1c36667292b5a1caf3567bb2da81434a2d5361242df69693253d973cf96b252e820f4e6000",
    "hash": "",
    "txid": "812ae7bdeed51e3eda4be0db46ae2a84ffe3680d5d69b9fc4a66c4980a5966f2",
    "depends": null,
    "fee": 0,
    "sigops": 0,
    "txtype": "1"
   }
  ],
  "minerpayout": [
   {
    "data": "0d0000000000000003c95a33477c17dad5448000001e781823399a0170cb3d644dfe4c496538fbb4967e28ba2be5b215b7caee661c",
    "hash": "",
    "txid": "",
    "depends": null,
    "fee": 0,
    "sigops": 0,
    "txtype": ""
   }
  ],
  "previousblockhash": "9301ef7440904ad1c6c8c92e017587f37f075763057fc239f9e9ee7f0b54ea63",
  "longpollid": "857eb80c681f36354b2e784869a89a1c",
  "target": "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
  "height": 11,
  "curtime": 1742478009,
  "version": 1,
  "bits": "01010000"
 }
```

**⚠️ Longpolling is not supported yet.**

### `POST /api/miner/submitblock`

Submits a block to the network. The block is expected to be either V1 or V2
Sia-encoded and then hex-encoded into a string.

***Example Request***:
```json
{
  "params": [
   "9301ef7440904ad1c6c8c92e017587f37f075763057fc239f9e9ee7f0b54ea630000000000000000b91adc670000000001000000000000000d0000000000000003c95a33477c17dad5448000001e781823399a0170cb3d644dfe4c496538fbb4967e28ba2be5b215b7caee661c010000000000000001000000000000001cf5f9f076dcf1749501dab9d891253a66fb3982066fd533ee97d4e7545200c700000000000000000100000000000000656432353531390000000000000000002000000000000000f2d4cfa8a5e3f77fd12674c942f849a85fbd5eb763327ee5164bd57768c82b95010000000000000002000000000000000b0000000000000052b7d2dcc80cd2e4000000cc58d6750fd0b836fc90c7a166c670749725dd9ebc54fe8d14879b303fb22cb10d0000000000000003c90772fc37cca71797800000cc58d6750fd0b836fc90c7a166c670749725dd9ebc54fe8d14879b303fb22cb10000000000000000000000000000000000000000000000000000000000000000000000000000000001000000000000000a00000000000000043c33c1937564800000000000000000000001000000000000001cf5f9f076dcf1749501dab9d891253a66fb3982066fd533ee97d4e7545200c7000000000000000000000000000000000100000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000004000000000000000af9d6897d047fe35db71969187d0d5ecfac50e09f7a105b191f50d1c36667292b5a1caf3567bb2da81434a2d5361242df69693253d973cf96b252e820f4e6000"
  ]
}
```

### Examples

Check out `TestMineGetBlockTemplate` in `api/api_test.go` for a Go example on
how to use these endpoints to mine a block and submit it.
