## 0.2.11 (2025-06-25)

### Fixes

- Update walletd dependency from 2.10.2 to 2.10.3

## 0.2.10 (2025-06-19)

### Fixes

- Update coreutils to v0.16.3 and core to v0.14.0

## 0.2.9 (2025-06-17)

### Fixes

- Update coreutils to v0.16.2 core to v0.13.2 and walletd to v2.10.2

## 0.2.8 (2025-06-16)

### Fixes

- Update `go.sia.tech/web/walletd` dependency from 0.29.3 to 0.30.0

## 0.2.7 (2025-06-16)

### Features

- Added [GET] /syncer/peers and [POST] /syncer/connect

## 0.2.6 (2025-06-14)

### Fixes

- Update core to v0.13.2 and coreutils to v0.16.1

## 0.2.5 (2025-06-10)

### Features

#### Add MaxTemplateAge as a config option to config file and CLI.

This allows for limiting the age of templates. When the max age is set to a
value > 0, templates will be invalidated once they reach the specified age.

### Fixes

- Update coreutils from 0.15.2 to 0.16.0.

## 0.2.2 (2025-06-02)

### Features

- Add 'commitment' field to `/getblocktemplate` endpoint.

## 0.2.1 (2025-05-27)

### Fixes

- Add migration code for consensus db.
