#!/usr/bin/env bash
# Spike S3: rotating rendezvous topics must all route to one shard.
#
# Runs bin/s3topics, then compares the pubsub topics the library actually
# routed to against the value computed locally by internal/topic.
set -uo pipefail

OUT=$(mktemp)
trap 'rm -f "$OUT"' EXIT

timeout 180 ./bin/s3topics "$@" >"$OUT" 2>&1
rc=$?
if [ $rc -ne 0 ]; then
    echo "s3topics exited $rc"
    tail -20 "$OUT"
    exit 1
fi

expected=$(grep -m1 '^S3_EXPECTED ' "$OUT" | awk '{print $2}')
# App/version prefix of our rotated topics, taken from what we actually published.
first_ct=$(grep -m1 '^S3_PUBLISHED ' "$OUT" | awk '{print $2}')
APP=$(echo "$first_ct" | cut -d/ -f2)
VER=$(echo "$first_ct" | cut -d/ -f3)
published=$(grep -c '^S3_PUBLISHED ' "$OUT")

# The library states where each message routed. Match only OUR publishes:
# a Core node relays other people's traffic across all 8 shards, so an
# unfiltered grep for pubsubTopic= sees the whole cluster.
mapfile -t observed < <(grep 'start publish Waku message' "$OUT" \
    | grep -F "contentTopic=/${APP}/${VER}/" \
    | grep -oE 'pubsubTopic=[^ ]+' | sed 's/pubsubTopic=//' | sort -u)

echo "expected shard      : $expected"
echo "topics published    : $published"
echo "distinct shards used: ${#observed[@]}"
for o in "${observed[@]}"; do echo "  $o"; done

if [ "$published" -lt 2 ]; then
    echo "S3 INCONCLUSIVE: fewer than 2 topics published"
    exit 2
fi
if [ "${#observed[@]}" -ne 1 ]; then
    echo "S3 FAIL: rotation spread across ${#observed[@]} shards — mesh would churn"
    exit 1
fi
if [ "${observed[0]}" != "$expected" ]; then
    echo "S3 FAIL: library routed to ${observed[0]}, internal/topic computed $expected"
    exit 1
fi

echo "S3 PASS: $published rotated topics all routed to $expected"
