#!/bin/sh
set -eu

capture_dir=${FAKE_MCT_CAPTURE:?}
status_file=${FAKE_MCT_STATUS:?}
answer_file=${FAKE_MCT_ANSWER:?}

if [ "$1" = "sync" ]; then
	sync_count_file="$capture_dir/sync-count"
	sync_count=0
	if [ -f "$sync_count_file" ]; then
		sync_count=$(cat "$sync_count_file")
	fi
	sync_count=$((sync_count + 1))
	printf '%s' "$sync_count" > "$sync_count_file"
	printf 'sync\n' >> "$capture_dir/events"
	exit 0
fi

if [ "$1" = "session" ] && [ "$2" = "show" ]; then
	cat "$status_file"
	exit 0
fi

if [ "$1" != "run" ]; then
	echo "unexpected command: $*" >&2
	exit 2
fi

printf 'run\n' >> "$capture_dir/events"
count_file="$capture_dir/count"
count=0
if [ -f "$count_file" ]; then
	count=$(cat "$count_file")
fi
count=$((count + 1))
printf '%s' "$count" > "$count_file"

shift
final_file=
text=
: > "$capture_dir/args-$count"
while [ "$#" -gt 0 ]; do
	printf '%s\n' "$1" >> "$capture_dir/args-$count"
	case "$1" in
		--text)
			shift
			text=$1
			;;
		--final-file)
			shift
			final_file=$1
			;;
	esac
	shift
done

printf '%s' "$text" > "$capture_dir/text-$count"
printf '%s' "${MACHTIANI_SESSION_ID-}" > "$capture_dir/session-env-$count"

if [ -z "$final_file" ]; then
	echo "missing --final-file" >&2
	exit 2
fi
cp "$answer_file" "$final_file"
