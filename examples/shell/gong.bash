# Source this file from Bash. Gong's CLI reads GONG_URL and
# GONG_API_TOKEN itself.

gong_notify() {
    command gong notify "$@"
}

_gong_html_escape() {
    local text=$1
    text=${text//&/\&amp;}
    text=${text//</\&lt;}
    text=${text//>/\&gt;}
    text=${text//\"/\&quot;}
    text=${text//\'/\&#39;}
    printf '%s' "$text"
}

_gong_notify_after_topic() {
    local topic=$1
    local label=$2
    shift 2
    local started=$SECONDS
    local command_status
    if "$@"; then
        command_status=0
    else
        command_status=$?
    fi
    local elapsed=$((SECONDS - started))
    local safe_label
    safe_label=$(_gong_html_escape "$label")
    local level result message
    if [ "$command_status" -eq 0 ]; then
        level=success
        result=Success
    else
        level=error
        result="Exit status: $command_status"
    fi
    printf -v message '<b>%s</b>\n%s\nElapsed: %ss' "$safe_label" "$result" "$elapsed"
    if ! command gong notify --topic "$topic" --level "$level" --category result -- "$message"; then
        printf '%s\n' 'Gong returned a nonzero status; command status is unchanged.' >&2
    fi
    return "$command_status"
}

gong_notify_after() {
    _gong_notify_after_topic result "$@"
}

gong_backup() {
    _gong_notify_after_topic backup "$@"
}

gong_training() {
    _gong_notify_after_topic training "$@"
}
