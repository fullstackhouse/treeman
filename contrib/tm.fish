# treeman fish shim — `tm` shell wrapper around `treeman wt`.
#
# `treeman worktree go` + `treeman worktree back` print resolved paths on
# stdout; this function wraps them so `tm foo` and `tm -` change
# directory in the parent shell.
#
# Install: source this file from your config.fish (or drop it in
# ~/.config/fish/functions/tm.fish):
#
#     source /path/to/treeman/contrib/tm.fish
#
# Usage:
#     tm PROJ-1234         # cd to existing worktree, or report missing
#     tm PROJ-1234 -c      # create + cd to a new worktree
#     tm -                 # cd back to main repo
#     tm - --remove        # cd back + drop current wt (if clean)
#     tm list              # passthrough to `treeman worktree list`
#     tm new FOO           # passthrough; useful when --create needs flags

function tm --description 'treeman shim: cd to worktrees'
  if test (count $argv) -eq 0
    treeman worktree list
    return
  end

  switch "$argv[1]"
    case - back
      set -e argv[1]
      set main (treeman worktree back $argv)
      if test $status -ne 0
        return $status
      end
      if test -n "$main"
        cd -- $main
      end
      return 0
    case list ls
      set -e argv[1]
      treeman worktree list $argv
      return
    case new create
      set -e argv[1]
      # `tm new BRANCH [...]` → forward to go --create so the
      # cd-after-create UX is the same as the bare `tm BRANCH -c`
      # form.
      set branch "$argv[1]"
      set -e argv[1]
      set target (treeman worktree go $branch --create $argv)
      if test $status -ne 0
        return $status
      end
      if test -n "$target"
        cd -- $target
      end
      return 0
    case -h --help help
      echo "tm — treeman shim"
      echo "  tm <name>           cd to existing worktree"
      echo "  tm <name> -c        create + cd to new worktree"
      echo "  tm new <name>       same as 'tm <name> -c'"
      echo "  tm - [--remove]     cd back to main repo (with --remove: drop current wt if clean)"
      echo "  tm list             list active worktrees"
      return 0
  end

  set name "$argv[1]"
  set -e argv[1]
  # Translate the short `-c` flag to the long `--create` form the Go
  # CLI exposes; everything else passes through.
  set args
  set want_create 0
  for a in $argv
    switch "$a"
      case -c --create
        set want_create 1
      case '*'
        set args $args $a
    end
  end
  if test $want_create -eq 1
    set args $args --create
  end

  set target (treeman worktree go $name $args)
  if test $status -ne 0
    return $status
  end
  if test -n "$target"
    cd -- $target
  end
end

# Completion: complete worktree slugs from `treeman worktree list`.
# NO_COLOR=1 suppresses ANSI escapes so the SLUG column parses cleanly.
function __tm_complete_worktrees
  NO_COLOR=1 treeman worktree list 2>/dev/null | awk 'NR>1 {print $2}'
end
complete -c tm -f -a "(__tm_complete_worktrees)" -d "worktree"
