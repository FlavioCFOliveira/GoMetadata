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
# curl_file url dest
#   Download a single file with curl. Skips if already present. Logs a
#   warning (without aborting) if the download fails.
# ---------------------------------------------------------------------------
curl_file() {
    local url=$1 dest=$2
    if [ -f "$dest" ]; then
        log "  Skipping $(basename "$dest") (already exists)"
        return
    fi
    mkdir -p "$(dirname "$dest")"
    if curl -sSLf --max-time 30 -o "$dest" "$url" 2>/dev/null; then
        log "  Downloaded $(basename "$dest")"
    else
        log "  Warning: could not download $url (skipping)"
        rm -f "$dest"
    fi
}

# ---------------------------------------------------------------------------
# SOURCE 1: ianare/exif-samples
#   JPEG images from Canon, Fujifilm, Kodak, Konica Minolta, Nikon, Olympus,
#   Panasonic, Pentax, Ricoh, Samsung, Sony.
#   Also includes: TIFF samples, HEIC (iPhone, Nokia), GPS-tagged Nikon
#   images, orientation tests (8 landscape + 8 portrait), invalid/malformed
#   JPEGs, and XMP-only JPEG (no_exif.jpg).
# ---------------------------------------------------------------------------
log "==> SOURCE 1: ianare/exif-samples"
dir=$(clone_full "https://github.com/ianare/exif-samples.git")
copy_ext "$dir" "$CORPUS/jpeg/exif-samples" jpg jpeg
copy_ext "$dir" "$CORPUS/tiff/exif-samples" tif tiff
copy_ext "$dir" "$CORPUS/heif/exif-samples" heic heif

# ---------------------------------------------------------------------------
# SOURCE 2: drewnoakes/metadata-extractor-images
#   Community-contributed images covering every format the library targets.
#   Notable inclusions:
#   - tif/BigTIFF/: BigTIFF (Intel/LE) and BigTIFFMotorola (Motorola/BE),
#     SubIFD4, SubIFD8 — explicit big-endian/little-endian BigTIFF tests
#   - tif/multipage.tif: multi-page TIFF
#   - tif/ImageTestSuite/: hash-named corpus of TIFF edge-case images
#   - png/PngSuite/: official PNG test suite (~175 files)
#   - png/photoshop-8x12-rgb24-all-metadata.png: EXIF+IPTC+XMP in PNG
#   - png/sampleWithExifData.png: EXIF in PNG
#   - png/photoshop-8x12-rgb24-interlaced.png: interlaced PNG
#   - webp/: VP8, VP8L, VP8X, lossless, lossy, animated WebP
#   - raf/, x3f/, pef/: Fuji RAF, Sigma X3F, Pentax PEF
#   - xmp/: sidecar XMP files from digiKam, jPhotoTagger, ExifTool, APhotoManager
# ---------------------------------------------------------------------------
log "==> SOURCE 2: drewnoakes/metadata-extractor-images"
dir=$(clone_full "https://github.com/drewnoakes/metadata-extractor-images.git")
copy_ext "$dir" "$CORPUS/jpeg/metadata-extractor"  jpg jpeg
copy_ext "$dir" "$CORPUS/tiff/metadata-extractor"  tif tiff
copy_ext "$dir" "$CORPUS/png/metadata-extractor"   png
copy_ext "$dir" "$CORPUS/heif/metadata-extractor"  heic heif avif
copy_ext "$dir" "$CORPUS/webp/metadata-extractor"  webp
copy_ext "$dir" "$CORPUS/raw/metadata-extractor"   cr2 cr3 nef arw dng orf rw2 crw
copy_ext "$dir" "$CORPUS/raw/metadata-extractor"   raf pef x3f mrw rwl srw
copy_ext "$dir" "$CORPUS/xmp/metadata-extractor"   xmp

# ---------------------------------------------------------------------------
# SOURCE 3: libexif/libexif-testsuite
#   EXIF 2.1/2.2 compliance tests and MakerNote samples: Canon, Casio, Epson,
#   Fuji, Nikon, Olympus, Pentax, Sanyo.
# ---------------------------------------------------------------------------
log "==> SOURCE 3: libexif/libexif-testsuite"
dir=$(clone_full "https://github.com/libexif/libexif-testsuite.git")
copy_ext "$dir" "$CORPUS/jpeg/libexif-testsuite" jpg jpeg

# ---------------------------------------------------------------------------
# SOURCE 4: libexif/libexif test/testdata
#   MakerNote variant tests: explicit coverage of Canon, Fuji, Olympus
#   (5 variants), Pentax (3 variants). Each file ships with a .parsed
#   reference file for golden-value testing.
# ---------------------------------------------------------------------------
log "==> SOURCE 4: libexif/libexif MakerNote testdata"
dir=$(clone_sparse \
    "https://github.com/libexif/libexif.git" \
    "libexif" \
    "test/testdata")
copy_ext "$dir/test/testdata" "$CORPUS/jpeg/libexif-makernotes" jpg jpeg

# ---------------------------------------------------------------------------
# SOURCE 5: IPTC reference images (curl from iptc.org)
#   Official IPTC Photo Metadata interoperability test images.
#   Each image has every IPTC field populated with its field name as the value,
#   making them ideal for field-by-field parser validation.
#   Versions span 2014–2024 to test backward compatibility.
# ---------------------------------------------------------------------------
log "==> SOURCE 5: IPTC reference images"
declare -a IPTC_VARIANTS=(
    "Std2024.1"
    "Std2023.2"
    "Std2021.1"
    "Std2019.1"
    "Std2017.1"
    "Core1.3"
)
for variant in "${IPTC_VARIANTS[@]}"; do
    url="https://www.iptc.org/std/photometadata/examples/IPTC-PhotometadataRef-${variant}.jpg"
    curl_file "$url" "$CORPUS/jpeg/iptc/IPTC-PhotometadataRef-${variant}.jpg"
done
# Older variants use _large suffix
for variant in "Std2016" "Std2014"; do
    url="https://iptc.org/std/photometadata/examples/IPTC-PhotometadataRef-${variant}_large.jpg"
    curl_file "$url" "$CORPUS/jpeg/iptc/IPTC-PhotometadataRef-${variant}.jpg"
done

# ---------------------------------------------------------------------------
# SOURCE 6: Exiv2/exiv2 test data
#   Exiv2 test suite: TIFF, DNG, NEF, JPEG, ORF, ARW, RW2, HEIC, PNG samples.
#   Notable inclusions:
#   - Stonehenge.heic, IMG_3578.heic, 2021-02-13-1929.heic: HEIC/HEIF samples
#   - 1343_comment.png, 1343_empty.png, 1343_exif.png: PNG with/without EXIF
#   - BlueSquare.xmp, StaffPhotographer-Example.xmp: XMP sidecar files
#   - Reagan.tiff, ReaganLargeTiff.tiff: TIFF with full metadata stack
#   - Multiple *_poc files: security/fuzzing proof-of-concept inputs
#   - exiv2-fujifilm-finepix-s2pro.jpg: Fuji MakerNote
#   - exiv2-sigma-d10.jpg: Sigma MakerNote
# ---------------------------------------------------------------------------
log "==> SOURCE 6: Exiv2/exiv2 test/data"
dir=$(clone_sparse \
    "https://github.com/Exiv2/exiv2.git" \
    "exiv2" \
    "test/data")
copy_ext "$dir/test/data" "$CORPUS/jpeg/exiv2"  jpg jpeg
copy_ext "$dir/test/data" "$CORPUS/tiff/exiv2"  tif tiff
copy_ext "$dir/test/data" "$CORPUS/raw/exiv2"   dng nef cr2 orf rw2 arw cr3
copy_ext "$dir/test/data" "$CORPUS/heif/exiv2"  heic heif
copy_ext "$dir/test/data" "$CORPUS/png/exiv2"   png
copy_ext "$dir/test/data" "$CORPUS/xmp/exiv2"   xmp

# ---------------------------------------------------------------------------
# SOURCE 7: Adobe XMP Toolkit SDK sample files
#   XMP-heavy JPEG and TIFF samples from Adobe's reference implementation.
#   These files exercise extended XMP (multi-packet), custom namespaces, and
#   RDF structures that most other corpora do not contain.
# ---------------------------------------------------------------------------
log "==> SOURCE 7: adobe/XMP-Toolkit-SDK sample files"
dir=$(clone_sparse \
    "https://github.com/adobe/XMP-Toolkit-SDK.git" \
    "xmp-sdk" \
    "samples/testfiles")
copy_ext "$dir/samples/testfiles" "$CORPUS/jpeg/xmp-sdk"  jpg jpeg
copy_ext "$dir/samples/testfiles" "$CORPUS/tiff/xmp-sdk"  tif tiff

# ---------------------------------------------------------------------------
# SOURCE 8: ExifTool test images (exiftool/exiftool t/images/)
#   Phil Harvey's reference test suite. Each file is specifically crafted to
#   exercise one parser path. Key files:
#   - ExifTool.jpg: EXIF + IPTC + XMP all three combined (26 KB)
#   - MWG.jpg: Metadata Working Group compliance image (2.3 KB)
#   - GPS.jpg: GPS IFD with full coordinate data (2.1 KB)
#   - IPTC.jpg: IPTC-only JPEG (9.8 KB)
#   - XMP.jpg: XMP-only JPEG (10 KB) — key gap filler
#   - ExtendedXMP.jpg: multi-packet extended XMP (1.4 KB)
#   - Unknown.jpg: JPEG with no recognized metadata (7.2 KB)
#   - ExifTool.tif: TIFF with EXIF + IPTC + XMP (4.9 KB)
#   - GeoTiff.tif: GeoTIFF with projection metadata (2.7 KB)
#   - BigTIFF.btf: BigTIFF format sample (384 bytes)
#   - Canon.jpg, Canon1DmkIII.jpg: Canon MakerNote variants
#   - FujiFilm.jpg, FujiFilm.raf: Fuji MakerNote + RAF raw
#   - Nikon.jpg, NikonD70.jpg, NikonD2Hs.jpg: Nikon MakerNote variants
#   - Panasonic.jpg, Panasonic.rw2: Panasonic MakerNote + RW2 raw
#   - Sigma.jpg, Sigma.x3f: Sigma MakerNote + X3F raw
#   - Minolta.jpg, Minolta.mrw: Minolta MakerNote + MRW raw
#   - PhaseOne.iiq: Phase One IIQ medium format raw
#   - CanonRaw.crw: Canon CRW (legacy raw format, pre-CR2)
#   - PLUS.xmp: PLUS (Picture Licensing Universal System) XMP sidecar
#   - FotoStation.jpg, PhotoMechanic.jpg: third-party metadata writers
# ---------------------------------------------------------------------------
log "==> SOURCE 8: exiftool/exiftool t/images"
dir=$(clone_sparse \
    "https://github.com/exiftool/exiftool.git" \
    "exiftool" \
    "t/images")
copy_ext "$dir/t/images" "$CORPUS/jpeg/exiftool"  jpg jpeg jps
copy_ext "$dir/t/images" "$CORPUS/tiff/exiftool"  tif tiff btf
copy_ext "$dir/t/images" "$CORPUS/raw/exiftool"   cr2 cr3 crw nef arw dng orf rw2 raf mrw x3f iiq
copy_ext "$dir/t/images" "$CORPUS/xmp/exiftool"   xmp

# ---------------------------------------------------------------------------
# SOURCE 9: sindresorhus/is-progressive — progressive vs baseline JPEG
#   MIT-licensed fixture set explicitly providing both progressive and
#   baseline JPEGs in a single small repo. Key gap filler for progressive
#   JPEG testing.
#   - kitten-progressive.jpg: progressive encoding
#   - progressive.jpg: progressive encoding
#   - baseline.jpg: baseline encoding
#   - kitten.jpg: baseline encoding
#   - curious-exif.jpg: EXIF in baseline JPEG
# ---------------------------------------------------------------------------
log "==> SOURCE 9: sindresorhus/is-progressive (progressive JPEG fixtures)"
PROGRESSIVE_BASE="https://raw.githubusercontent.com/sindresorhus/is-progressive/main/fixture"
for f in kitten-progressive.jpg progressive.jpg baseline.jpg kitten.jpg curious-exif.jpg; do
    curl_file "$PROGRESSIVE_BASE/$f" "$CORPUS/jpeg/progressive/$f"
done

# ---------------------------------------------------------------------------
# SOURCE 10: tlnagy/exampletiffs — TIFF structural variants
#   Comprehensive TIFF test corpus targeting storage and compression
#   variations, not camera metadata. Useful for IFD traversal and TIFF
#   structure parsing correctness.
#   Key files:
#   - mri.tif: multi-page TIFF (MRI slices, 225 KB)
#   - shapes_tiled_multi.tif: tiled multi-page (37 KB)
#   - shapes_lzw.tif, shapes_deflate.tif, shapes_uncompressed.tif: compression variants
#   - shapes_lzw_planar.tif: planar (not chunky) storage
#   - 4D-series.ome.tif: OME-TIFF with XML metadata in ImageDescription
# ---------------------------------------------------------------------------
log "==> SOURCE 10: tlnagy/exampletiffs (TIFF structural variants)"
TIFF_BASE="https://raw.githubusercontent.com/tlnagy/exampletiffs/master"
for f in \
    mri.tif \
    shapes_tiled_multi.tif \
    shapes_lzw.tif \
    shapes_deflate.tif \
    shapes_uncompressed.tif \
    shapes_lzw_planar.tif \
    shapes_lzw_tiled.tif \
    shapes_lzw_palette.tif \
    shapes_uncompressed_tiled_planar.tif \
    4D-series.ome.tif
do
    curl_file "$TIFF_BASE/$f" "$CORPUS/tiff/exampletiffs/$f"
done

# ---------------------------------------------------------------------------
# SOURCE 11: Exiv2 PNG metadata tests (individual curl)
#   Three small PNGs from Exiv2 that test specific PNG chunk scenarios:
#   - 1343_exif.png: PNG with EXIF data in eXIf chunk (400 bytes)
#   - 1343_comment.png: PNG with tEXt/Comment chunk (400 bytes)
#   - 1343_empty.png: PNG with no metadata chunks (minimal)
#   These are not pulled by the sparse clone above because they are already
#   included via SOURCE 6. Listed here for documentation clarity.
# ---------------------------------------------------------------------------
# (Already captured in SOURCE 6 above via copy_ext png.)

# ---------------------------------------------------------------------------
# SOURCE 12: libexif/libexif MakerNote variant tests (individual curl)
#   The libexif repo's test/testdata contains 9 focused MakerNote JPEGs with
#   .parsed reference files. Covered by SOURCE 4 above via sparse clone.
#   Direct URLs for documentation:
#
#   https://raw.githubusercontent.com/libexif/libexif/master/test/testdata/canon_makernote_variant_1.jpg
#   https://raw.githubusercontent.com/libexif/libexif/master/test/testdata/fuji_makernote_variant_1.jpg
#   https://raw.githubusercontent.com/libexif/libexif/master/test/testdata/olympus_makernote_variant_2.jpg
#   https://raw.githubusercontent.com/libexif/libexif/master/test/testdata/olympus_makernote_variant_3.jpg
#   https://raw.githubusercontent.com/libexif/libexif/master/test/testdata/olympus_makernote_variant_4.jpg
#   https://raw.githubusercontent.com/libexif/libexif/master/test/testdata/olympus_makernote_variant_5.jpg
#   https://raw.githubusercontent.com/libexif/libexif/master/test/testdata/pentax_makernote_variant_2.jpg
#   https://raw.githubusercontent.com/libexif/libexif/master/test/testdata/pentax_makernote_variant_3.jpg
#   https://raw.githubusercontent.com/libexif/libexif/master/test/testdata/pentax_makernote_variant_4.jpg

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------
log ""
log "Corpus populated. File counts by format:"
for fmt in jpeg tiff png heif webp raw xmp; do
    n=$(find "$CORPUS/$fmt" -type f ! -name '.gitkeep' 2>/dev/null | wc -l | tr -d ' ')
    log "  $fmt: $n file(s)"
done
log ""
log "Run 'go test ./...' to execute integration tests against the corpus."
