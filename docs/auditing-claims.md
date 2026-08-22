# Auditing claims against code

A recurring review with one question: **does any comment or document here assert
a property the code does not deliver?**

Not a code review. Not style, naming, missing tests, or bugs in isolation. Only
the gap between a confident sentence and an implementation that does not enforce
it.

## Why this specific question

On 2026-08-22 a self-review of the relay code found four issues and named its own
largest weakness: the author had written what he was examining. A fresh reader,
asked only the question above, then found three more — including two the review
had walked straight past:

- `Tag` was fixed to be per-relay, the review blessed it as done, and the
  register frame beside it still carried the device's mesh identity in
  cleartext. Two relay operators could still link a device across relays, which
  is precisely the property the fix existed to deliver.
- The memory-exhaustion vector removed that morning was reintroduced the same
  day, one layer out, in a map filled in after a four-byte magic check.

Every one was visible from the prose alone. The reason they survived is that an
assumption held while writing survives re-reading, because the assumption is
doing the reading.

## The prompt

Run this against a fresh agent — one with no history of writing the code.

> Find places where a COMMENT or DOCUMENT asserts a security, privacy, or
> behavioural property that the CODE DOES NOT ACTUALLY DELIVER.
>
> This is not a general code review. Do not report style, naming, missing tests,
> or possible bugs in isolation. Report ONLY mismatches between a stated claim
> and what the implementation does.
>
> METHOD:
> 1. Read the claim first. Write down, in your own words, the precise property it
>    asserts and what would have to be true for it to hold.
> 2. Then read the implementation and check whether it enforces exactly that.
> 3. Pay special attention to claims containing: cannot, never, always, only,
>    unrelated, opaque, impossible, guaranteed, indistinguishable, bounded, at
>    most.
> 4. Check the INPUTS to derivations. A hash is only as private as the values
>    that go into it, and the field *beside* it can undo the whole property.
> 5. Be suspicious of claims about what a THIRD PARTY cannot learn or do.
>
> RULES: verify before reporting; follow the call chain. State uncertainty
> explicitly — "I could not determine X" is useful, a confident wrong finding is
> not. Quote the claim with file:line and the code that fails it. Say concretely
> what an attacker or operator could do that the comment says they cannot. Do not
> modify files. Finding none is a good result; do not invent findings to seem
> useful.

Give it the two examples above. A worked case of the shape teaches it more than
any amount of description.

## When to run it

- Before tagging a release, since a version number invites people to rely on
  this.
- After anything that adds a party to the threat model, as blind relays did.
- After a security fix, because "fixed" is exactly when nobody looks again — two
  of the findings above hid behind a fix that had been declared complete.

## What it does not replace

An outside human review. This catches claims that contradict the code beside
them; it does not catch a design that is coherent, well-documented, and wrong.
