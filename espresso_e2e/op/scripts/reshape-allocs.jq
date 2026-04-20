#!/bin/bash

jq -S '
def pad_hex: .[2:] as $hex
  | (64 - ($hex | length)) as $padding
  | "0x" + ("0" * $padding) + $hex ;

. | map_values({
    state:  {
      nonce: .nonce,
      code: .code,
      balance: .balance,
      storage: .storage | with_entries({key: .key|pad_hex, value : .value|pad_hex}),
    },
    name: .name,
})
' "$@"
