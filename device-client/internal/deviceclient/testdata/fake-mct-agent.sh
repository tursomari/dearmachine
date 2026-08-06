#!/bin/sh
set -eu

capture_dir=${FAKE_MCT_CAPTURE:?}
status_file=${FAKE_MCT_STATUS:?}
answer_file=${FAKE_MCT_ANSWER:?}

if [ "$1" = "sync" ]; then
	if [ -n "${FAKE_MCT_SYNC_DELAY-}" ]; then
		sleep "$FAKE_MCT_SYNC_DELAY"
	fi
	if [ -n "${FAKE_MCT_SYNC_ERROR-}" ]; then
		printf '%s\n' "$FAKE_MCT_SYNC_ERROR" >&2
	fi
	if [ "${FAKE_MCT_SYNC_EXIT-0}" -ne 0 ]; then
		exit "$FAKE_MCT_SYNC_EXIT"
	fi
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
	if [ -n "${FAKE_MCT_SHOW_DELAY-}" ]; then
		sleep "$FAKE_MCT_SHOW_DELAY"
	fi
	if [ -n "${FAKE_MCT_SHOW_ERROR-}" ]; then
		printf '%s\n' "$FAKE_MCT_SHOW_ERROR" >&2
	fi
	if [ "${FAKE_MCT_SHOW_EXIT-0}" -ne 0 ]; then
		exit "$FAKE_MCT_SHOW_EXIT"
	fi
	cat "$status_file"
	exit 0
fi

if [ "$1" = "session" ] && [ "$2" = "fork" ]; then
	if [ -n "${FAKE_MCT_FORK_ERROR-}" ]; then
		printf '%s\n' "$FAKE_MCT_FORK_ERROR" >&2
	fi
	if [ "${FAKE_MCT_FORK_EXIT-0}" -ne 0 ]; then
		exit "$FAKE_MCT_FORK_EXIT"
	fi
	printf '%s\n' "$3" >> "$capture_dir/forked-sessions"
	printf '%s\n' "${FAKE_MCT_FORK_ID-forked-session}"
	exit 0
fi

if [ "$1" = "session" ] && [ "$2" = "delete" ]; then
	printf '%s\n' "$3" >> "$capture_dir/deleted-sessions"
	exit 0
fi

if [ "$1" != "run" ]; then
	echo "unexpected command: $*" >&2
	exit 2
fi

printf 'run\n' >> "$capture_dir/events"
if [ -n "${FAKE_MCT_RUN_DELAY-}" ]; then
	sleep "$FAKE_MCT_RUN_DELAY"
fi
if [ -n "${FAKE_MCT_RUN_ERROR-}" ]; then
	printf '%s\n' "$FAKE_MCT_RUN_ERROR" >&2
fi
if [ "${FAKE_MCT_RUN_EXIT-0}" -ne 0 ]; then
	exit "$FAKE_MCT_RUN_EXIT"
fi
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
printf '%s' "${DEARMACHINE_BACKENDS-}" > "$capture_dir/backends-env-$count"
printf '%s' "${DEARMACHINE_BACKEND-}" > "$capture_dir/backend-env-$count"
printf '%s' "${AGENT_MANAGER_PATH-}" > "$capture_dir/manager-env-$count"

if [ -z "$final_file" ]; then
	echo "missing --final-file" >&2
	exit 2
fi
if [ "${FAKE_MCT_SKIP_FINAL-0}" -eq 0 ]; then
	cp "$answer_file" "$final_file"
fi
