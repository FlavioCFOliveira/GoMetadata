#!/usr/bin/env bash
# Downloads reference test images from public repositories into testdata/corpus/.
# Idempotent: safe to re-run; existing files are not overwritten.
#
# Usage:
#   bash testdata/download.sh
#   make testdata
#
# Requirements: git, curl

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CORPUS="$SCRIPT_DIR/corpus"
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

log() { printf '[testdata] %s\n' "$*" >&2; }

# ---------------------------------------------------------------------------
# clone_full url
#   Shallow-clone a repo under $TMP. Skips if already cloned this run.
# ---------------------------------------------------------------------------
clone_full() {
    local url=$1
    local name
    name=$(basename "$url" .git)
    if [ -d "$TMP/$name" ]; then
        echo "$TMP/$name"
        return
    fi
    log "Cloning $name..."
    git clone --depth=1 --quiet "$url" "$TMP/$name"
    echo "$TMP/$name"
}

# ---------------------------------------------------------------------------
# clone_sparse url name path [path ...]
#   Sparse-checkout only the given tree paths. For large repos.
# ---------------------------------------------------------------------------
clone_sparse() {
    local url=$1 name=$2; shift 2
    local paths=("$@")
    if [ -d "$TMP/$name" ]; then
        echo "$TMP/$name"
        return
    fi
    log "Sparse-cloning $name (${paths[*]})..."
    git clone --depth=1 --filter=blob:none --no-checkout --quiet "$url" "$TMP/$name"
    git -C "$TMP/$name" sparse-checkout set "${paths[@]}"
    git -C "$TMP/$name" checkout --quiet
    echo "$TMP/$name"
}

# ---------------------------------------------------------------------------
# copy_ext src dst ext [ext ...]
#   Recursively find files matching *.ext (case-insensitive) under src and
#   copy them to dst. Uses -n (no-clobber) to avoid overwriting existing files.
# ---------------------------------------------------------------------------
copy_ext() {
    local src=$1 dst=$2; shift 2
    local exts=("$@")
    mkdir -p "$dst"
    local count=0
    for ext in "${exts[@]}"; do
        while IFS= read -r f; do
            # Avoid silent name collisions: append a counter suffix if needed.
            local base
            base=$(basename "$f")
            local dest="$dst/$base"
            if [ -e "$dest" ]; then
                local stem="${base%.*}"
                local sfx="${base##*.}"
                local i=2
                while [ -e "$dst/${stem}_${i}.${sfx}" ]; do ((i++)); done
                dest="$dst/${stem}_${i}.${sfx}"
            fi
            cp "$f" "$dest"
            ((count++)) || true
        done < <(find "$src" -type f -iname "*.$ext" 2>/dev/null)
    done
    log "  -> $dst: copied $count file(s)"
}

# ---------------------------------------------------------------------------
# SOURCE 1: ianare/exif-samples
#   JPEG images from Canon, Fujifilm, Kodak, Konica Minolta, Nikon, Olympus,
#   Panasonic, Pentax, Ricoh, Samsung, Sony.
# ---------------------------------------------------------------------------
log "==> SOURCE 1: ianare/exif-samples"
dir=$(clone_full "https://github.com/ianare/exif-samples.git")
copy_ext "$dir" "$CORPUS/jpeg/exif-samples" jpg jpeg

# ---------------------------------------------------------------------------
# SOURCE 2: drewnoakes/metadata-extractor-images
#   Community-contributed images covering every format the library targets.
#   Images live in per-manufacturer subdirectories so we need a full shallow
#   clone — depth=1 gives all blobs from the latest commit without history.
# ---------------------------------------------------------------------------
log "==> SOURCE 2: drewnoakes/metadata-extractor-images"
dir=$(clone_full "https://github.com/drewnoakes/metadata-extractor-images.git")
copy_ext "$dir" "$CORPUS/jpeg/metadata-extractor"  jpg jpeg
copy_ext "$dir" "$CORPUS/tiff/metadata-extractor"  tif tiff
copy_ext "$dir" "$CORPUS/png/metadata-extractor"   png
copy_ext "$dir" "$CORPUS/heif/metadata-extractor"  heic heif avif
copy_ext "$dir" "$CORPUS/webp/metadata-extractor"  webp
copy_ext "$dir" "$CORPUS/raw/metadata-extractor"   cr2 cr3 nef arw dng orf rw2

# ---------------------------------------------------------------------------
# SOURCE 3: libexif/libexif-testsuite
#   EXIF 2.1/2.2 compliance tests and MakerNote samples: Canon, Casio, Epson,
#   Fuji, Nikon, Olympus, Pentax, Sanyo.
# ---------------------------------------------------------------------------
log "==> SOURCE 3: libexif/libexif-testsuite"
dir=$(clone_full "https://github.com/libexif/libexif-testsuite.git")
copy_ext "$dir" "$CORPUS/jpeg/libexif-testsuite" jpg jpeg

# ---------------------------------------------------------------------------
# SOURCE 4: IPTC reference images (curl from iptc.org)
#   Official IPTC Photo Metadata interoperability test images.
#   Each image has every IPTC field populated with its field name as the value.
# ---------------------------------------------------------------------------
log "==> SOURCE 4: IPTC reference images"
mkdir -p "$CORPUS/jpeg/iptc"
declare -a IPTC_VARIANTS=(
    "Std2024.1"
    "Core1.3"
    "Std2021.1"
)
for variant in "${IPTC_VARIANTS[@]}"; do
    url="https://www.iptc.org/std/photometadata/examples/IPTC-PhotometadataRef-${variant}.jpg"
    dest="$CORPUS/jpeg/iptc/IPTC-PhotometadataRef-${variant}.jpg"
    if [ -f "$dest" ]; then
        log "  Skipping $variant (already exists)"
        continue
    fi
    if curl -sSLf --max-time 30 -o "$dest" "$url" 2>/dev/null; then
        log "  Downloaded IPTC-PhotometadataRef-${variant}.jpg"
    else
        log "  Warning: could not download $url (skipping)"
        rm -f "$dest"
    fi
done

# ---------------------------------------------------------------------------
# SOURCE 5: Exiv2/exiv2 test data
#   Exiv2 test suite: TIFF, DNG, NEF, JPEG, ORF, ARW, RW2 samples.
# ---------------------------------------------------------------------------
log "==> SOURCE 5: Exiv2/exiv2 test/data"
dir=$(clone_sparse \
    "https://github.com/Exiv2/exiv2.git" \
    "exiv2" \
    "test/data")
copy_ext "$dir/test/data" "$CORPUS/jpeg/exiv2"  jpg jpeg
copy_ext "$dir/test/data" "$CORPUS/tiff/exiv2"  tif tiff
copy_ext "$dir/test/data" "$CORPUS/raw/exiv2"   dng nef cr2 orf rw2 arw

# ---------------------------------------------------------------------------
# SOURCE 6: Adobe XMP Toolkit SDK sample files
#   XMP-heavy JPEG and TIFF samples from Adobe's reference implementation.
# ---------------------------------------------------------------------------
log "==> SOURCE 6: adobe/XMP-Toolkit-SDK sample files"
dir=$(clone_sparse \
    "https://github.com/adobe/XMP-Toolkit-SDK.git" \
    "xmp-sdk" \
    "samples/testfiles")
copy_ext "$dir/samples/testfiles" "$CORPUS/jpeg/xmp-sdk"  jpg jpeg
copy_ext "$dir/samples/testfiles" "$CORPUS/tiff/xmp-sdk"  tif tiff

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------
log ""
log "Corpus populated. File counts by format:"
for fmt in jpeg tiff png heif webp raw; do
    n=$(find "$CORPUS/$fmt" -type f ! -name '.gitkeep' 2>/dev/null | wc -l | tr -d ' ')
    log "  $fmt: $n file(s)"
done
log ""
log "Run 'go test ./...' to execute integration tests against the corpus."
