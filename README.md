# chainpulse

[![ci](https://github.com/Urthella/chainpulse/actions/workflows/ci.yml/badge.svg)](https://github.com/Urthella/chainpulse/actions/workflows/ci.yml)

Health monitoring for blockchain RPC endpoints across **15 chains and 9 protocols**: is each endpoint up, how high is its head, how far behind its peers is it, and has it stopped moving. One Go binary, no dependencies, Prometheus metrics.

```bash
go install github.com/Urthella/chainpulse@latest
chainpulse -once
```

Real output, 29 Oct 2026, built-in public endpoints:

```text
CHAIN      ENDPOINT     HEIGHT         UNIT               BEHIND  LATENCY  STATUS
aptos      aptoslabs    1,146,932,054  block              0       300ms    ok
arbitrum   official     519,333,201    block              2       320ms    ok
arbitrum   publicnode   519,333,203    block              0       225ms    ok
avalanche  official     98,856,967     block              0       191ms    ok
base       official     53,240,665     block              0       252ms    ok
base       publicnode   53,240,665     block              0       221ms    ok
bitcoin    blockstream  973,682        block              0       709ms    ok
bitcoin    mempool      973,682        block              0       7.715s   ok
cosmos     publicnode   33,631,312     block              0       303ms    ok
ethereum   publicnode   26,297,879     block              0       203ms    ok
hyperevm   official     49,830,849     block              0       410ms    ok
near       fastnear     222,114,194    block              5       325ms    ok
near       official     222,114,199    block              0       3.325s   ok
optimism   official     158,835,950    block              0       251ms    ok
polygon    publicnode   96,376,866     block              0       214ms    ok
solana     official     462,851,942    slot               0       247ms    ok
starknet   publicnode   17,122,340     block              0       207ms    ok
sui        graphql      339,389,256    checkpoint         0       342ms    ok
ton        toncenter    102,066,503    masterchain block  0       292ms    ok
```

`-once` exits `1` if any endpoint is down, so it works as a cron job or CI check too.

## Protocols

Every chain comes down to one request and a path into the response:

| Kind | Chains | Request | Height |
|---|---|---|---|
| `evm` | Ethereum, Base, Arbitrum, Optimism, Polygon, Avalanche, HyperEVM | `eth_blockNumber` | `result` (hex) |
| `solana` | Solana | `getSlot` (confirmed) | `result` |
| `starknet` | Starknet | `starknet_blockNumber` | `result` |
| `near` | NEAR | `block` (final) | `result.header.height` |
| `cometbft` | Cosmos Hub, any CometBFT chain | `status` | `result.sync_info.latest_block_height` |
| `sui` | Sui | GraphQL `checkpoint { sequenceNumber }` | `data.checkpoint.sequenceNumber` |
| `aptos` | Aptos | `GET /v1` | `block_height` |
| `toncenter` | TON | `GET /getMasterchainInfo` | `result.last.seqno` |
| `esplora` | Bitcoin | `GET /blocks/tip/height` | plain text |

Sui uses GraphQL because public Sui fullnodes [retired JSON-RPC](https://docs.sui.io/develop/accessing-data/json-rpc-migration): calling the old method now returns an error, which chainpulse reports as one instead of reading it as height 0. Adding an EVM chain is one line of config; adding a protocol is one entry in [probe.go](probe.go).

## Watch mode

```bash
chainpulse -listen :9090 -interval 30s -config my-endpoints.json
```

| Metric | |
|---|---|
| `chainpulse_up` | 1 if the last probe returned a height |
| `chainpulse_height` | latest height (block, slot, checkpoint or masterchain seqno) |
| `chainpulse_behind` | how far behind the highest endpoint of the same chain |
| `chainpulse_head_age_seconds` | time since the height last went up; a stuck node keeps growing it even when it's alone |
| `chainpulse_latency_seconds` | duration of the last probe |
| `chainpulse_probes_failed_total` | probes that returned no height |

All carry `chain`, `endpoint` and `kind` labels. `GET /status` returns the same as JSON.

Config is a JSON list of `{"chain", "name", "kind", "url"}`; [endpoints.json](endpoints.json) is the built-in one. Point it at your own nodes and paid providers to compare them against public endpoints.

## Deploy

```bash
docker build -t chainpulse .
docker run -p 9090:9090 chainpulse        # or: -v $PWD/my.json:/my.json chainpulse -config /my.json
```

The image is a static binary on distroless, running as non-root.

[deploy/alerts.yml](deploy/alerts.yml) has Prometheus alert rules:

| Alert | Fires when |
|---|---|
| `ChainRPCDown` | an endpoint hasn't answered for 2 minutes |
| `ChainRPCLagging` | an endpoint stays more than 30 heights behind its best peer for 5 minutes |
| `ChainHeadStuck` | the head hasn't moved for 5 minutes, or 1 hour on Bitcoin, where 30 quiet minutes are normal |

The rules have their own unit tests ([alerts.test.yml](deploy/alerts.test.yml)), run in CI with `promtool test rules`: an endpoint down for 1 minute doesn't page, Ethereum stuck for 20 minutes does, and Bitcoin at the same 20 minutes doesn't.

## Develop

```bash
nix develop
go test -race ./...
CHAINPULSE_LIVE=1 go test -run TestLive -v ./...   # probe the real endpoints
```

The parser tests run on real responses captured from mainnet endpoints of all nine protocols ([testdata](testdata)), including Sui's deprecation error.

## License

[MIT](LICENSE)
