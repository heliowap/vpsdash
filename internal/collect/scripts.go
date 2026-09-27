package collect

const RunnerUnitsScript = `set -eu
XDG_RUNTIME_DIR="/run/user/$(id -u)"
DBUS_SESSION_BUS_ADDRESS="unix:path=$XDG_RUNTIME_DIR/bus"
export XDG_RUNTIME_DIR DBUS_SESSION_BUS_ADDRESS
systemctl --user show-environment >/dev/null
for path in "$HOME"/.config/systemd/user/actions.runner.*.service; do
  [ -e "$path" ] || continue
  unit=${path##*/}
  load=$(systemctl --user show "$unit" --property=LoadState --value)
  state=$(systemctl --user show "$unit" --property=ActiveState --value)
  printf '%s\t%s\t%s\n' "$unit" "$load" "$state"
done
unit=gh-agents-cleanup.timer
load=$(systemctl --user show "$unit" --property=LoadState --value 2>/dev/null || printf 'not-found')
state=$(systemctl --user show "$unit" --property=ActiveState --value 2>/dev/null || printf 'inactive')
printf '%s\t%s\t%s\n' "$unit" "$load" "$state"
`

const MetricsScript = `set -eu
awk '/^cpu / { printf "cpu"; for (i=2;i<=8;i++) printf " %s", $i; printf "\n" }' /proc/stat
awk '/^(MemTotal|MemAvailable):/ { print $1, $2 }' /proc/meminfo
df -P /
cat /proc/uptime
`

const DiscoveryScript = `
docker ps --format 'docker\t{{.Names}}\t{{.Status}}' 2>/dev/null || true
systemctl list-units --type=service --state=running --no-legend --plain 2>/dev/null | awk '{print "systemd\t" $1 "\tactive"}' || true
systemctl --user list-units --type=service --state=running --no-legend --plain 2>/dev/null | awk '{print "systemd\tuser:" $1 "\tactive"}' || true
tmux list-sessions -F 'tmux	#{session_name}	active' 2>/dev/null || true
`

const SessionsScript = `
tmux list-panes -a -F '#{session_name}	#{pane_pid}	#{pane_current_command}	#{pane_current_path}	#{pane_id}' 2>/dev/null || true
printf '%s\n' '--PROCESSES--'
ps -eo pid=,ppid=,stat=,times=,comm=,args=
printf '%s\n' '--SCREENS--'
deadline=$(($(date +%s) + 4))
for pane in $(tmux list-panes -a -F '#{pane_id}' 2>/dev/null | head -n 50); do
  [ "$(date +%s)" -lt "$deadline" ] || break
  screen=$(timeout 1 tmux capture-pane -p -t "$pane" 2>/dev/null) || continue
  sum=$(printf '%s' "$screen" | cksum)
  last=$(printf '%s\n' "$screen" | awk 'NF { line = $0 } END { print substr(line, 1, 160) }')
  printf '%s\t%s\t%s\n' "$pane" "${sum%% *}" "$last"
done
`
