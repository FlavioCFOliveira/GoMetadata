---
name: reference-iim-field-constraints
description: Confirmed IIM 4.2 field constraints for key Record 2 datasets — max lengths, repeatability, date/time formats
metadata:
  type: reference
---

Confirmed from ExifTool tag tables and exiv2.org against IIM 4.2 specification.

## Record 1 (Envelope)
| DS | Name | Max Bytes | Repeatable |
|----|------|-----------|------------|
| 90 | CodedCharacterSet | 3 | No |

## Record 2 (Application) — key datasets
| DS | Name | Max Bytes | Repeatable | Notes |
|----|------|-----------|------------|-------|
| 5  | ObjectName | 64 | No | |
| 25 | Keywords | 64 | **Yes** | each occurrence is one keyword |
| 55 | DateCreated | 8 | No | CCYYMMDD (ISO 8601 digits only) |
| 60 | TimeCreated | 11 | No | HHMMSS±HHMM (e.g. 143022+0100) |
| 62 | DigitalCreationDate | 8 | No | CCYYMMDD |
| 63 | DigitalCreationTime | 11 | No | HHMMSS±HHMM |
| 80 | By-line | 32 | **Yes** | repeatable (multiple authors) |
| 105 | Headline | 256 | No | |
| 116 | CopyrightNotice | 128 | No | |
| 120 | Caption/Abstract | 2000 | No | |

## Extended-length encoding threshold (IIM §1.6.2)
Standard form: 2-byte length, values 0–32767 (high bit = 0).
Extended form: high bit of size field = 1; lower 15 bits = byte count of actual length; max 4-byte length supported.
Encoder should switch to extended form at n >= 0x8000 (32768). The library does this correctly.

## CodedCharacterSet UTF-8 marker (IIM §1.5.1)
Value: 0x1B 0x25 0x47 (ESC % G = ISO 2022 UTF-8 designator).
Length: exactly 3 bytes.
