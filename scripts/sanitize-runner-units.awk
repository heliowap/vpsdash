BEGIN { FS = OFS = "\t" }

$1 == "gh-agents-cleanup.timer" && NF == 3 {
  print $1, $2, $3
  next
}

$1 ~ /^actions\.runner\.[A-Za-z0-9._-]+\.service$/ && NF == 3 {
  count++
  print "actions.runner.captured-" count ".service", $2, $3
  next
}

{ invalid = 1 }

END { if (invalid) exit 1 }
