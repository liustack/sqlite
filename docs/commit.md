---
summary: 'Commit rules: message format, the type list, atomic commits, generated files, branches and merges'
read_when:
  - Getting ready to commit
  - Writing a commit message or a merge message
  - Splitting up a set of changes
---

# Commit rules

## Format

```
<type>[scope]: <imperative summary>

<optional body: why the change was made, and trade-offs that are not obvious>
```

- The summary is at most 72 characters, with no period at the end.
- type: `feat` `fix` `docs` `style` `refactor` `perf` `test` `build` `ci` `chore` `revert`.
- scope is the package or area, such as `engine`, `libc`, `gen`, `driver`, `docs`.
- For a breaking change, add `!` after the type, or write `BREAKING CHANGE:` at the end of the body.
- No semicolons, and no dashes as punctuation.
- No signature or co-author lines.

## Atomic commits

- One commit does one thing, and can be verified and reverted on its own.
- A behavior change and its tests go in the same commit.
- Pure refactors, formatting and renames are committed on their own.
- A change to `engine/`, to the generator or to `scripts/gen-engine.sh` goes in the same commit as the generated files it changes, once they are checked in. Never edit generated files by hand.
- Do not commit build output: `.build/`, `*.test`, `*.out`.

## Branches and merges

- Work on a branch (`git switch -c <branch>`), not on `main`.
- A branch goes into `main` with a merge commit (`git merge --no-ff`), whose message is `merge: <what the branch does>`. `git log --first-parent main` then reads as the list of changes.
- Run [the checks](testing.md#before-committing) before every commit.
