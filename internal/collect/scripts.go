package collect

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
tmux list-panes -a -F '#{session_name}	#{pane_pid}	#{pane_current_command}	#{pane_current_path}' 2>/dev/null || true
printf '%s\n' '--PROCESSES--'
ps -eo pid=,ppid=,comm=,args=
`
