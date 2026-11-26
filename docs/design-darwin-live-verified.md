# DES-086 amendment: Darwin live-verified handoff gates

**Decision (Jim, 2026-11-25):** “Live check, tree must match”.

CoreSimulator and app-hosted XCTest use services outside the Darwin
snapshot sandbox. For those gates, the approved alternative is to
explicitly opt in to running the unchanged command on the real
checkout, with a verified tree before and after execution. This is not
permission to run an ordinary snapshot gate on the live checkout when
snapshot preparation fails. This amendment specifies the contract;
it does not claim that the runner or its guard implementation has shipped.

## Sidecar and verdict contract

Keep the check YAML unchanged so v5.2.3 can read it. The per-check entry
in the snapshot sidecar at `.punt-labs/ethos/checks/snapshots/<slug>.yaml`
has a top-level `isolation` field next to `profile`. Use the existing
worktree profile value `linux-worktree-v1`: it is the sidecar schema
version, not a host name, and is used on both Linux and Darwin. The new
field, not another profile name, selects the mode. The allowed values are
`snapshot` (the default) and `live-verified`; any other value is a
validation error. The resolved mode is part of the key, cache record,
and verdict log. A Linux runner seeing `live-verified` visibly defers;
it neither rejects the sidecar nor executes the gate in snapshot mode.

As in DES-085, policy comes from the session's canonical policy store,
not the edited checkout. Because live verification runs unsandboxed,
only an operator-approved policy revision may opt in. A mutable sidecar
in the worktree (including when it is also the canonical checkout) is
not approval. If the runner cannot establish approval, visibly defer
before launch.

The runner's optional `requires` list declares `pty`, `posix_shm`,
or `mach_lookup`; unknown entries are validation errors, and the
declaration is keyed. For snapshot mode, Darwin visibly defers any of
these requirements before launch and grants none of them. Linux
satisfies PTY and POSIX shm privately and visibly defers Mach with
`mach_lookup unavailable in linux snapshot`. Live verification is a
separately selected Darwin mode, not an automatic fallback from these
deferrals.

## Live verification

Apply the same admission and time budget as for snapshot checks. Arm
both guards before taking the opening tree key and keep them active
through the closing key and cutoff. Run the unchanged gate on the live
checkout. Publish or reuse a pass or failure only when the keys agree,
both guards establish coverage through the cutoff, and neither vetoes
under the rules below. A detected change, unresolved coverage, or
inability to arm or drain either guard visibly defers; never cache
it as a terminal result. The verdict log uses
`isolation: live-verified`, not snapshot isolation.

1. **FSEvents:** enable file-level events and watch keyed paths and Git
   metadata through an event ID sampled after taking the closing key.
   Delivery may be late or coalesced. Treat `MustScanSubDirs`,
   `UserDropped`, `KernelDropped`,
   and `RootChanged` as vetoes. If the stream cannot establish coverage
   through the cutoff, defer.
2. **kqueue:** arm an independent `EVFILT_VNODE` guard for every keyed
   file and Git metadata including `HEAD`, `index`, and refs. Write,
   extend, rename, delete and revoke events veto. Replacement and
   unlink also veto. Ignore `NOTE_ATTRIB` only if atime changed and
   mode, UID, GID, size, mtime, ctime, device, inode and link count are
   unchanged. Otherwise veto, including on chmod. This avoids a false
   deferral from merely reading `.git/index`.
   Also open `EVFILT_VNODE` on each directory containing a keyed path,
   up to and including the checkout root and `.git`. Include existing
   directories where a newly created file could enter the keyed set,
   even if they contain no keyed files yet. Watch directory-entry
   changes with `NOTE_WRITE` and `NOTE_LINK`, and also register
   `NOTE_EXTEND`, `NOTE_RENAME`, `NOTE_DELETE`, and `NOTE_REVOKE`.
   The strict rule (a) is to defer on any unattributed directory write,
   including at the checkout root. Neither an equal closing key nor
   FSEvents reporting only ignored paths attributes a directory event.
   One coalesced note is enough to veto an untracked, non-ignored file
   created and removed between equal keys.

   Profiles should direct outputs into dedicated ignored directories
   with no keyed paths. Create those directories before the opening
   scan so the gate writes inside unwatched directories rather than
   creating their entries in watched parents.
3. **Descriptor budget:** reserve one fd per keyed file, Git metadata
   guard, and watched directory. Before adding directory guards, the
   counts were about 285 vnode fds on Quarry and 437 modeled on Koch.
   Budget against the runner's actual soft `RLIMIT_NOFILE` with headroom
   for existing and transient fds. Both sets exceed a soft limit of 256
   even before directory guards or headroom. If the full set cannot be
   armed within the budget, defer visibly with the required count and
   available budget; never drop paths to fit.

### Watcher evidence

In [Quarry's cutoff probe](https://github.com/punt-labs/quarry-menubar/tree/757566bd4126e455a04c836751f3328bf19033bf/docs/ethos-live-evidence),
raw FSEvents reported no loss or coalescing flags but missed changes
made after the closing key and before the cutoff:

| Change | Missed by FSEvents |
| --- | ---: |
| Keyed-file write | 13/20 |
| `HEAD` replacement | 3/20 |
| `index` replacement | 4/20 |

kqueue caught 60/60 and 300/300 transient probes. In the
[attribute and descriptor follow-up](https://github.com/punt-labs/quarry-menubar/tree/786f39a524fe80e3c6e894077fe74ea51a7c4e19/docs/ethos-live-evidence),
the guard caught chmod 20/20 on a keyed file and 20/20 on `.git/index`;
the clean gate passed 3/3 under that earlier probe.

In the [directory-guard follow-up](https://github.com/punt-labs/quarry-menubar/pull/89),
with FSEvents disabled, the directory watch deferred 20/20 times on
an untracked source created and removed between key samples. At the
checkout root, XcodeGen replacing the Git-ignored `.xcodeproj` and a
transient root `.swift` file both produced vnode note 18 and equal
keys. The transient source nevertheless changed `make lint` (exit 2).
Attribution based on FSEvents was rejected because it can miss an event
without a loss flag.

Under the strict live-verified rule, Quarry's clean gate always
visibly defers, never passes: XcodeGen replaces the root `.xcodeproj`
on every run. Directing XcodeGen output into a dedicated ignored
subdirectory or using `xcodegen --use-cache` are possible remedies;
the choice belongs to Quarry and is outside this slice.

Keep the index veto strict. In the
[optional-lock and index burst probes](https://github.com/punt-labs/quarry-menubar/tree/5d94f8305a1c21362e2a06689e89692f08278dae/docs/ethos-live-evidence),
FSEvents undercounted 20/20 bursts at cutoff without a loss flag and
the `.git` directory vnode gave only one notification per burst. A
content-based exemption could miss an intermediate index version.
The strict veto caught `git add` 20/20 and stage-then-unstage 20/20;
the latter had equal opening and closing keys.

| Background Git activity during Quarry's clean gate | Spurious deferrals |
| --- | ---: |
| No concurrent Git | 0/10 |
| `git status` and `git diff --stat` every 1–3 s, default locks | 10/10 |
| Same commands, actor sets `GIT_OPTIONAL_LOCKS=0` | 0/10 |

The separate Ethos follow-up sets `GIT_OPTIONAL_LOCKS=0` in the agent
session environments it configures (Claude Code settings `env` and
OpenCode config) and re-measures through that setup. Editors, terminals
and other Git processes outside those environments can still cause
deferrals. The mitigation does not weaken the veto.

### Why not relax the snapshot sandbox?

Quarry's app-hosted XCTest stopped after 2.47 s with Xcode exit 70,
denied PowerManagement Mach access and launchd job creation. Allowing
PowerManagement alone reached a testmanagerd denial after 2.95 s
(Xcode 65, error 159). Allowing both testmanagerd endpoints then hit
denied `/dev/ptmx` writes, ioctl (`_IO "t" 84`) and pseudo-terminal
setup. These probes do not establish that job creation alone is fatal.
The unchanged SwiftPM manifest sandbox also failed to apply inside
the outer sandbox with seeded dependencies. Quarry's live controls
passed in 78.43 s and failed on the deliberate XCTest break in 25.99 s
(Xcode 65); neither had a corresponding isolated snapshot verdict.

Koch's simulator probe needed CoreSimulatorService, SimLaunchHost,
simdiskimaged, PowerManagement and Instruments Mach lookups, and still
hit PTY and launchd denials. Its XCTest canary could read the live
checkout while the sandboxed Xcode process was denied access to the
same path. Exact Mach endpoints do not bound what unsandboxed services
can do on the check's behalf.

A parent-owned broker was considered but not selected. The private
simulator-set probe created one iPhone 17 Pro (iOS 26.5), then removed
the exact device after the dummy child was killed (exit 137); no device
was booted and no app was installed. Neither a supplied UUID nor a
private set constrains arbitrary app or path requests on the daemon.
A broker would need to own and verify the device, runtime, snapshot-built
app and cleanup. It would not by itself solve app-hosted macOS XCTest.

## Darwin toolchain trust and residuals

- For the system Make shim, select
  `DEVELOPER_DIR=/Library/Developer/CommandLineTools` only if the directory
  and all ancestors are root-owned and not group- or other-writable.
  Recheck trust before launch, key the toolchain identity, and allow
  read and executable access to the verified tools. Keep the host
  developer selector denied in this case. Missing or untrusted CLT
  visibly defers. Do not trust the full Xcode bundle merely because it
  is root-owned: on the test Mac its parent `/Applications` is
  `root:admin 0775` and the runner is in `admin`. A name replaced and
  restored between keys is not covered by endpoint comparison alone.
- Live-verified gates run unsandboxed: they can read unkeyed host files
  and ignored checkout inputs, such as DerivedData and local config.
  Writes to keyed paths veto rather than pass; active editing can
  cause deferrals. These limits must not be reported as snapshot
  isolation.
- Darwin snapshot `id -un` returns the UID; no name resolution without
  Directory Services. Do not grant a Mach exception for identity.
- Darwin snapshot localhost rules are not Linux's private network
  namespace. Even exact port filters let a check reach an outside
  listener on `127.0.0.1` and `::1`. A sandbox bind collided with the
  host listener (`EADDRINUSE`); after the host listener closed the same
  policy allowed an in-sandbox bind and connect. Keep non-loopback
  denial and log
  `isolation: residual (host localhost services reachable, shared port space)`
  for such verdicts.
