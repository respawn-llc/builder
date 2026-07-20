# Session event format size audit

This report contains aggregate, anonymized evidence from transforming the ten largest stable eligible legacy session logs with the production v0-to-v1 transformer. One larger candidate was excluded because its checksum and modification time changed during measurement.

## Size results

| Session | Legacy bytes | V1 bytes | Reduction |
| --- | ---: | ---: | ---: |
| S01 | 1,274,768,012 | 1,045,455,393 | 17.9886% |
| S02 | 558,763,435 | 466,517,988 | 16.5089% |
| S03 | 332,354,894 | 276,154,928 | 16.9096% |
| S04 | 313,740,108 | 262,555,935 | 16.3142% |
| S05 | 290,619,041 | 239,261,464 | 17.6718% |
| S06 | 185,419,485 | 153,903,731 | 16.9970% |
| S07 | 175,237,214 | 139,155,084 | 20.5904% |
| S08 | 130,524,507 | 103,315,182 | 20.8461% |
| S09 | 109,732,989 | 91,727,668 | 16.4083% |
| S10 | 78,604,188 | 62,851,121 | 20.0410% |

Aggregate size changed from **3,449,763,873 bytes** to **2,840,898,494 bytes**, removing **608,865,379 bytes (17.6495%)**.

## Event-kind bytes

Legacy source:

| Kind | Bytes |
| --- | ---: |
| `tool_completed` | 1,912,477,751 |
| `message` | 1,348,252,832 |
| `history_replaced` | 101,644,542 |
| `cache_request_observed` | 33,010,493 |
| `cache_response_observed` | 38,573,513 |
| `cache_warning` | 161,703 |
| `local_entry` | 14,830,698 |

Dropped readerless legacy kinds:

| Kind | Bytes |
| --- | ---: |
| `goal_cleared` | 5,216 |
| `goal_set` | 50,900 |
| `goal_status_updated` | 263,558 |
| `model_recovery_consumed` | 96,379 |
| `model_recovery_pending` | 97,775 |
| `prompt_history` | 47,482 |
| `run_finished` | 124,838 |
| `run_started` | 126,193 |

V1 output:

| Kind | Bytes |
| --- | ---: |
| `tool_completed` | 1,323,754,140 |
| `message` | 1,341,388,719 |
| `history_replaced` | 101,670,595 |
| `cache_request_observed` | 29,114,377 |
| `cache_response_observed` | 31,823,735 |
| `cache_warning` | 132,799 |
| `local_entry` | 13,013,659 |
| Ten headers | 470 |

## Provider snapshot evidence

- Authoritative direct snapshots: **130,338**
- Direct generated-Raw snapshots: **0**
- Absent snapshots: **0**
- Correlation artifacts: **0**
- Lexical Raw retained byte-for-byte: **651,974,252 bytes**
- Generated Raw: **0 bytes**
- Legacy provider mirrors: **1,196,772,527 bytes**
- Compact v1 provider snapshots: **610,306,057 bytes**
- Provider mirror bytes removed: **586,466,470 bytes**

All ten selected sessions used the direct snapshot path and produced zero correlation temporary I/O.

## Resource and immutability evidence

The maximum live resource measurements across the selected sessions were:

- Inline value bytes: **0**
- Source decoder bytes: **65,536**
- Encoder/merge bytes: **65,536**
- Open spool/run files: **0**
- Spool bytes: **0**

Every live/current resource counter returned to zero after each transform. Each selected source retained identical size, checksum, and modification time across transformation and complete v1 read-back.

This artifact contains no session identifiers, names, filesystem paths, transcript content, or reusable audit command.
