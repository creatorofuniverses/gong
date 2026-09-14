# Source this file from Fish. Gong's CLI reads GONG_URL and
# GONG_API_TOKEN itself.

function gong_notify
    command gong notify $argv
end

function __gong_html_escape --argument-names text
    string replace -a '&' '&amp;' -- "$text" |
        string replace -a '<' '&lt;' |
        string replace -a '>' '&gt;' |
        string replace -a '"' '&quot;' |
        string replace -a "'" '&#39;'
end

function __gong_notify_after_topic --argument-names topic label
    set -e argv[1..2]
    set -l started (date +%s)
    command $argv
    set -l command_status $status
    set -l elapsed (math (date +%s) - $started)
    set -l safe_label (__gong_html_escape "$label" | string collect)
    set -l level
    set -l result
    if test $command_status -eq 0
        set level success
        set result Success
    else
        set level error
        set result "Exit status: $command_status"
    end
    set -l message (printf '<b>%s</b>\n%s\nElapsed: %ss' "$safe_label" "$result" "$elapsed" | string collect)
    if not command gong notify --topic "$topic" --level "$level" --category result -- "$message"
        printf '%s\n' 'Gong returned a nonzero status; command status is unchanged.' >&2
    end
    return $command_status
end

function gong_notify_after
    __gong_notify_after_topic result $argv
end

function gong_backup
    __gong_notify_after_topic backup $argv
end

function gong_training
    __gong_notify_after_topic training $argv
end
