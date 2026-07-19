#!/usr/bin/env bash

set -euo pipefail

readonly SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly PROJECT_ROOT="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
readonly COVERAGE_FILE="${PROJECT_ROOT}/coverage.out"
readonly README_FILE="${PROJECT_ROOT}/Readme.md"
readonly BEGIN_MARKER='<!-- coverage:begin -->'
readonly END_MARKER='<!-- coverage:end -->'

if [ ! -s "$COVERAGE_FILE" ]; then
    echo "ERROR: coverage profile is missing or empty: $COVERAGE_FILE" >&2
    exit 1
fi

if ! awk -v begin="$BEGIN_MARKER" -v end="$END_MARKER" '
    $0 == begin {
        begin_count++
        if (end_count > 0) {
            invalid_order = 1
        }
    }
    $0 == end {
        end_count++
        if (begin_count != 1) {
            invalid_order = 1
        }
    }
    END {
        if (begin_count != 1 || end_count != 1 || invalid_order) {
            exit 1
        }
    }
' "$README_FILE"; then
    echo "ERROR: Readme.md must contain exactly one ordered coverage marker pair" >&2
    exit 1
fi

readonly TEMP_DIR="$(mktemp -d)"
trap 'rm -rf -- "$TEMP_DIR"' EXIT

readonly STATS_FILE="${TEMP_DIR}/stats.tsv"
readonly PACKAGES_FILE="${TEMP_DIR}/packages.tsv"
readonly CONTENT_FILE="${TEMP_DIR}/coverage.md"
readonly UPDATED_README="${TEMP_DIR}/Readme.md"

awk -v expected_prefix='fastrg-controller/internal/' '
    BEGIN {
        FS = "[[:space:]]+"
        OFS = "\t"
    }

    NR == 1 {
        if ($0 !~ /^mode: (set|count|atomic)$/) {
            print "ERROR: invalid coverage profile mode" > "/dev/stderr"
            failed = 1
            exit 1
        }
        next
    }

    {
        if (NF != 3 || $2 !~ /^[0-9]+$/ || $3 !~ /^[0-9]+$/) {
            print "ERROR: malformed coverage profile entry at line " NR > "/dev/stderr"
            failed = 1
            exit 1
        }

        block = $1
        file = block
        if (sub(/:[0-9]+\.[0-9]+,[0-9]+\.[0-9]+$/, "", file) != 1 ||
            index(file, expected_prefix) != 1 || file !~ /\.go$/) {
            print "ERROR: unsupported coverage profile path at line " NR > "/dev/stderr"
            failed = 1
            exit 1
        }

        package_name = file
        sub(/^fastrg-controller\//, "", package_name)
        sub(/\/[^/]+\.go$/, "", package_name)
        if (package_name !~ /^internal\/.+/) {
            print "ERROR: could not determine package at line " NR > "/dev/stderr"
            failed = 1
            exit 1
        }

        statement_count = $2 + 0
        if (block in block_statements && block_statements[block] != statement_count) {
            print "ERROR: inconsistent duplicate block at line " NR > "/dev/stderr"
            failed = 1
            exit 1
        }

        block_statements[block] = statement_count
        block_package[block] = package_name
        if ($3 + 0 > 0) {
            block_covered[block] = 1
        }
        entry_count++
    }

    END {
        if (failed) {
            exit 1
        }
        if (entry_count == 0) {
            print "ERROR: coverage profile has no entries" > "/dev/stderr"
            exit 1
        }

        for (block in block_statements) {
            package_name = block_package[block]
            statements[package_name] += block_statements[block]
            total_statements += block_statements[block]
            if (block_covered[block]) {
                covered[package_name] += block_statements[block]
                total_covered += block_statements[block]
            }
        }

        if (total_statements == 0) {
            print "ERROR: coverage profile contains no statements" > "/dev/stderr"
            exit 1
        }

        for (package_name in statements) {
            print "package", package_name, covered[package_name] + 0, statements[package_name]
        }
        print "total", total_covered + 0, total_statements
    }
' "$COVERAGE_FILE" > "$STATS_FILE"

awk -F '\t' '$1 == "package" {
    percentage = 100 * $3 / $4
    printf "%.12f\t%s\t%.1f%%\n", percentage, $2, percentage
}' "$STATS_FILE" | LC_ALL=C sort -t $'\t' -k1,1nr -k2,2 > "$PACKAGES_FILE"

IFS=$'\t' read -r _ total_covered total_statements < <(
    awk -F '\t' '$1 == "total" { print; exit }' "$STATS_FILE"
)
readonly TOTAL_PERCENTAGE="$(awk -v covered="$total_covered" -v statements="$total_statements" '
    BEGIN { printf "%.1f", 100 * covered / statements }
')"

{
    printf 'The following results were measured on %s with disposable etcd, PostgreSQL, and Kafka containers and all three `TEST_*` variables set:\n\n' "$(date +%F)"
    printf '| Package | Coverage |\n'
    printf '|---|---:|\n'
    while IFS=$'\t' read -r _ package_name percentage; do
        printf '| `%s` | %s |\n' "$package_name" "$percentage"
    done < "$PACKAGES_FILE"
    printf '| **Merged total** | **%s%%** |\n\n' "$TOTAL_PERCENTAGE"
    printf 'Each percentage is the statement coverage of that package by the entire test suite, calculated from a single merged coverage profile.\n'
} > "$CONTENT_FILE"

awk -v begin="$BEGIN_MARKER" -v end="$END_MARKER" -v content="$CONTENT_FILE" '
    $0 == begin {
        print
        while ((getline line < content) > 0) {
            print line
        }
        close(content)
        replacing = 1
        next
    }
    $0 == end {
        replacing = 0
        print
        next
    }
    !replacing { print }
' "$README_FILE" > "$UPDATED_README"

chmod --reference="$README_FILE" "$UPDATED_README"
mv -- "$UPDATED_README" "$README_FILE"
