#!/usr/bin/env python3
"""Verify a source-only snapshot. No credentials, Git mounts, or runtime state."""
import argparse
import hashlib
from pathlib import Path
import shutil
import subprocess
import tempfile
import urllib.request

IMAGE = 'ghcr.io/viperproject/gobra@sha256:d9dc17cdb3725818943a6224872628610e130c349e059ad393ef4f88878cca19'
TLC_URL = 'https://github.com/tlaplus/tlaplus/releases/download/v1.7.4/tla2tools.jar'
TLC_SHA = '936a262061c914694dfd669a543be24573c45d5aa0ff20a8b96b23d01e050e88'
ROOT = Path(__file__).resolve().parents[2]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--docker-host', help='Explicit Docker socket, if needed')
    parser.add_argument('--tlc-jar', type=Path, help='Pre-downloaded pinned tla2tools.jar')
    parser.add_argument('--output', type=Path, required=True, help='New artifact directory outside the checkout')
    parser.add_argument('--expand', action='store_true', help='Also explore multiple scopes and shared provider entries')
    parser.add_argument('--outbound-only', action='store_true', help='Check outbound approval plus recovery and feedback contracts')
    parser.add_argument('--recovery-only', action='store_true', help='Check only outbound recovery and feedback')
    args = parser.parse_args()
    output = args.output.resolve()
    if output == ROOT or ROOT in output.parents:
        parser.error('artifacts must be outside the checkout')
    output.mkdir(parents=True, exist_ok=False)
    shutil.copyfile(Path(__file__), output / 'run.py')
    (output / 'tools.txt').write_text(f'Gobra image: {IMAGE}\nTLC: {TLC_URL}\nTLC SHA256: {TLC_SHA}\n')
    def record_artifacts():
        files = sorted(path for path in output.iterdir() if path.name != 'SHA256SUMS')
        (output / 'SHA256SUMS').write_text(''.join(
            f'{hashlib.sha256(path.read_bytes()).hexdigest()}  {path.name}\n' for path in files))
    docker = ['docker'] + (['--host', args.docker_host] if args.docker_host else [])
    subprocess.run(docker + ['image', 'inspect', IMAGE], check=True, stdout=subprocess.DEVNULL)
    with tempfile.TemporaryDirectory(prefix='dearmachine-guest-proof-') as name:
        snapshot = Path(name)
        data = args.tlc_jar.read_bytes() if args.tlc_jar else urllib.request.urlopen(TLC_URL, timeout=60).read()
        if hashlib.sha256(data).hexdigest() != TLC_SHA:
            raise SystemExit('TLC checksum mismatch')
        (snapshot / 'tla2tools.jar').write_bytes(data)
        for file in ('Guest.tla', 'Guest.cfg', 'Participation.tla', 'Participation.cfg', 'Replacement.tla', 'Replacement.cfg', 'Outbound.tla', 'Outbound.cfg', 'OutboundRecovery.tla', 'OutboundRecovery.cfg'):
            shutil.copyfile(ROOT / 'verification/guest' / file, snapshot / file)
            shutil.copyfile(snapshot / file, output / file)
        source = (ROOT / 'dearmachine/internal/client/guest_policy.go').read_text()
        (output / 'guest_policy.go').write_text(source)
        (output / 'source.sha256').write_text(hashlib.sha256(source.encode()).hexdigest() + '\n')
        # Gobra receives the exact production declarations and bodies; only
        # annotation comment prefixes are removed. No independent proof copy.
        (snapshot / 'policy.gobra').write_text(source.replace('// @ ', ''))
        common = docker + ['run', '--rm', '--network', 'none', '--mount', f'type=bind,src={snapshot},dst=/proof,readonly']
        def run(label, command, expected=0, marker=None):
            result = subprocess.run(command, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True, timeout=600)
            (output / (label + '.log')).write_text(result.stdout)
            if result.returncode != expected or (marker and marker not in result.stdout):
                raise SystemExit(f'{label} failed: exit {result.returncode}; inspect {output / (label + ".log")}')
            print(f'{label}: expected exit {expected}', flush=True)
        tlc = common + ['--entrypoint', 'java', IMAGE, '-XX:+UseParallelGC', '-Xmx2g', '-cp', '/proof/tla2tools.jar', 'tlc2.TLC', '-workers', '2', '-metadir', '/tmp/tlc']
        recovery = (snapshot / 'OutboundRecovery.cfg').read_text()
        recovery_invariants = next(line for line in recovery.splitlines()
                                   if line.startswith('INVARIANTS '))

        def recovery_check(label, cfg, invariant=None, temporal=False):
            path = label + '.cfg'
            if invariant and not temporal:
                # Witnesses and mutations retain ALL independent safety checks.
                if invariant not in recovery_invariants.split():
                    cfg = cfg.replace(recovery_invariants, recovery_invariants + ' ' + invariant)
            (snapshot / path).write_text(cfg)
            shutil.copyfile(snapshot / path, output / path)
            expected = 13 if temporal else 12 if invariant else 0
            marker = ('Temporal properties were violated' if temporal else
                      f'Invariant {invariant} is violated' if invariant else
                      'Model checking completed. No error has been found.')
            run(label, tlc + ['-config', '/proof/' + path, '/proof/OutboundRecovery.tla'],
                expected, marker)

        for kind in ('preview', 'submission'):
            cfg = recovery.replace('Kind = "submission"', f'Kind = "{kind}"')
            recovery_check('recovery-' + kind, cfg)
            for witness in ('NoTypedRecovery', 'NoLostReconciliation', 'NoLateReceipt', 'NoUnknownHold',
                            'NoSafeRedraft', 'NoExhaustedHold', 'NoExpiredHold',
                            'NoRevokedUnknownHold', 'NoHeldNoticeFailure'):
                recovery_check('recovery-' + kind + '-' + witness, cfg, witness)
        # One attempt per phase bounds the sequential fixture; the independent
        # preview/submission fixtures above retain the full three-attempt budget.
        phases = recovery.replace('PhaseFlow = FALSE', 'PhaseFlow = TRUE')
        phases = phases.replace('MaxAttempts = 3', 'MaxAttempts = 1')
        recovery_check('recovery-phases', phases)
        for witness in ('NoTwoPhaseNotices', 'NoFailedPreviewThenSubmissionNotice',
                        'NoTransientThenTwoPhaseNotices', 'NoTransientHold',
                        'NoInterruptedPreviewThenSubmissionNotice',
                        'NoPendingTransientThenSubmissionNotice'):
            recovery_check('recovery-phases-' + witness, phases, witness)
        for mutant, invariant in {
            'SharedPhaseNotice': 'NoticePhaseIdentity',
            'SuppressPhaseNotice': 'NoticeAvailability',
            'NonSendingNotice': 'NoticeEligibility',
            'PendingTransientNotice': 'NoticeEligibility',
            'ForgetPreviewNotice': 'NoticeAtMostOnce',
            'ForgetSubmissionNotice': 'NoticeAtMostOnce',
        }.items():
            recovery_check('recovery-phases-' + mutant,
                           phases.replace('Mutation = "None"', f'Mutation = "{mutant}"'),
                           invariant)
        recovery_check('recovery-NoInterruptedNotice', recovery, 'NoInterruptedNotice')
        guaranteed = recovery.replace('VerifiedWindow = FALSE', 'VerifiedWindow = TRUE')
        recovery_check('recovery-verified-window', guaranteed)
        recovery_check('recovery-NoGuaranteedRecovery', guaranteed, 'NoGuaranteedRecovery')
        for case in ('malformed', 'stale', 'ambiguous', 'bare'):
            cfg = recovery.replace('DecisionCase = "malformed"', f'DecisionCase = "{case}"')
            recovery_check('recovery-feedback-' + case, cfg, 'NoInvalidFeedback')
        premature = recovery.replace('DecisionCase = "malformed"', 'DecisionCase = "premature"')
        recovery_check('recovery-premature', premature)
        recovery_check('recovery-NoPrematureDeferral', premature, 'NoPrematureDeferral')
        recovery_check('recovery-ConsumePremature',
                       premature.replace('Mutation = "None"', 'Mutation = "ConsumePremature"'),
                       'PrematureDeferred')
        for mutant, invariant in {
            'BlindRetry': 'RetryEvidence',
            'AbsentMeansRejected': 'RetryEvidence',
            'ExpiredRetry': 'BoundedAttempts',
            'SkipGrantCheck': 'FreshEligibility',
            'RenewDeadline': 'DurableDeadline',
            'MutableRequest': 'FrozenRequest',
            'RedraftUnknown': 'SafeRedraft',
            'PublicNotice': 'NoticePrivacy',
            'ForgetNotice': 'NoticeAtMostOnce',
            'FeedbackApproves': 'NoInventedApproval',
        }.items():
            cfg = recovery.replace('Mutation = "None"', f'Mutation = "{mutant}"')
            recovery_check('recovery-' + mutant, cfg, invariant)
        isolated = recovery.replace('Records = {r1}', 'Records = {"broken", "healthy"}')
        isolated = isolated.replace('Isolation = FALSE', 'Isolation = TRUE')
        isolated = isolated.replace('SPECIFICATION Spec', 'SPECIFICATION IsolationSpec')
        recovery_check('recovery-isolation-witness', isolated, 'NoIsolation')
        isolated += '\nPROPERTY Progress\n'
        recovery_check('recovery-isolation', isolated)
        recovery_check('recovery-GlobalFailure',
                       isolated.replace('Mutation = "None"', 'Mutation = "GlobalFailure"'),
                       temporal=True)
        if args.recovery_only:
            record_artifacts()
            return
        outbound = (snapshot / 'Outbound.cfg').read_text()
        outbound_invariants = next(line for line in outbound.splitlines()
                                   if line.startswith('INVARIANTS '))
        def outbound_check(label, cfg, expected_invariant=None, witness=False):
            # Keep all independent properties enabled. Mutation expectations name
            # the specific violated state property; there is no Safety flag.
            if witness:
                cfg = cfg.replace(outbound_invariants,
                                  outbound_invariants + ' ' + expected_invariant)
            path = label + '.cfg'
            (snapshot / path).write_text(cfg)
            shutil.copyfile(snapshot / path, output / path)
            expected = 12 if expected_invariant else 0
            marker = (f'Invariant {expected_invariant} is violated' if expected
                      else 'Model checking completed. No error has been found.')
            run(label, tlc + ['-config', '/proof/' + path, '/proof/Outbound.tla'], expected, marker)
        outbound_check('outbound', outbound)
        mutations = {
            'PublicPreview': 'PreviewPrivacy',
            'MissingBanner': 'PreviewPrivacy',
            'IncompletePreview': 'PreviewPrivacy',
            'StalePreview': 'PreviewEligibility',
            'ForgedOwner': 'ApprovalEvidence',
            'WrongOwner': 'ApprovalEvidence',
            'WrongRequest': 'ApprovalEvidence',
            'WrongScopeRequest': 'ApprovalEvidence',
            'StaleDecision': 'ApprovalEvidence',
            'InvalidDecision': 'ApprovalEvidence',
            'NoMeansYes': 'ApprovalEvidence',
            'IgnoreRevocation': 'SubmissionEligibility',
            'StaleGeneration': 'SubmissionEligibility',
            'BypassApproval': 'ApprovalEvidence',
            'ReuseReplyApproval': 'ApprovalEvidence',
            'SendRejected': 'ApprovalEvidence',
            'ChangeBody': 'ApprovedDisclosure',
            'ChangeAttachments': 'ApprovedDisclosure',
            'AddRecipient': 'ApprovedDisclosure',
            'BannerInRelease': 'ReleaseWithoutBanner',
            'MutableRetry': 'RetryIdentity',
            'RestartApproves': 'ApprovalEvidence',
            'ApproveUnseen': 'ApprovedWhatWasPreviewed',
        }
        for mutant, invariant in mutations.items():
            cfg = outbound.replace('Mutation = "None"', f'Mutation = "{mutant}"')
            if mutant in ('WrongOwner', 'WrongScopeRequest'):
                cfg = cfg.replace('Scopes = {s1}', 'Scopes = {s1, s2}')
                cfg = cfg.replace('GuestMessages = {g1}', 'GuestMessages = {}')
            if mutant == 'AddRecipient':
                cfg = cfg.replace('Guests = {a}', 'Guests = {a, b}')
                cfg = cfg.replace('GuestMessages = {g1}', 'GuestMessages = {}')
            if mutant == 'BypassApproval':
                # One mutation, separately exercised for both message origins.
                for origin, without in (('owner', 'GuestMessages = {g1}'),
                                        ('guest', 'OwnerMessages = {o1}')):
                    fixture = cfg.replace(without, without.split(' = ')[0] + ' = {}')
                    outbound_check('outbound-' + mutant + '-' + origin, fixture, invariant)
            else:
                outbound_check('outbound-' + mutant, cfg, invariant)
        for witness in ('NoSharedOwner', 'NoSharedGuest', 'NoPrivate', 'NoRejection',
                        'NoReapprovedDraft', 'NoRepeatedAnswers', 'NoIndependentAnswer',
                        'NoReinvitedAnswer', 'NoBoundedHold'):
            outbound_check('outbound-' + witness, outbound, witness, witness=True)
        if args.expand:
            scoped = outbound.replace('Scopes = {s1}', 'Scopes = {s1, s2}')
            scoped = scoped.replace('GuestMessages = {g1}', 'GuestMessages = {}')
            outbound_check('outbound-scopes', scoped)
            multiple = outbound.replace('Guests = {a}', 'Guests = {a, b}')
            multiple = multiple.replace('GuestMessages = {g1}', 'GuestMessages = {}')
            outbound_check('outbound-multi-guest', multiple)
            for witness in ('NoMultiGuest', 'NoReducedRecipients'):
                outbound_check('outbound-' + witness, multiple, witness, witness=True)
        if args.outbound_only:
            record_artifacts()
            return
        run('gobra', common + [IMAGE, '-i', '/proof/policy.gobra'], marker='Gobra found 0 errors')
        replacement = (snapshot / 'Replacement.cfg').read_text()
        for fixture, mutation in (
                ('Clean', 'None'), ('LegacyUnsent', 'None'), ('LegacySent', 'None'),
                ('Conflict', 'None'), ('Clean', 'RepollPending'),
                ('LegacySent', 'ForgetReceipt'), ('LegacySent', 'RunSavedResult'),
                ('Conflict', 'LooseRepair'), ('LegacySent', 'NoLegacyRepair')):
            label = 'replacement-' + fixture + '-' + mutation
            cfg = replacement.replace('Fixture = "Clean"', f'Fixture = "{fixture}"')
            cfg = cfg.replace('Mutation = "None"', f'Mutation = "{mutation}"')
            # Safety counterexamples must not be mistaken for liveness failures.
            if fixture == 'Conflict' or mutation not in ('None', 'NoLegacyRepair'):
                cfg = cfg.replace('PROPERTY Progress', '')
            path = label + '.cfg'
            (snapshot / path).write_text(cfg)
            shutil.copyfile(snapshot / path, output / path)
            expected = 0 if mutation == 'None' else (13 if mutation == 'NoLegacyRepair' else 12)
            marker = ('Model checking completed. No error has been found.' if expected == 0
                      else 'Temporal properties were violated' if expected == 13
                      else 'Invariant Safety is violated')
            run(label, tlc + ['-config', '/proof/' + path, '/proof/Replacement.tla'], expected, marker)
        config = (snapshot / 'Guest.cfg').read_text()
        participation = (snapshot / 'Participation.cfg').read_text()
        def workflow(label, cfg, invariant=None):
            path = label + '.cfg'
            if invariant:
                cfg = cfg.replace('INVARIANTS Safety TypeOK AnswerPrivacy',
                                  'INVARIANTS Safety TypeOK AnswerPrivacy ' + invariant)
            (snapshot / path).write_text(cfg)
            shutil.copyfile(snapshot / path, output / path)
            expected = 12 if invariant or 'Mutation = "None"' not in cfg else 0
            marker = (f'Invariant {invariant or "Safety"} is violated'
                      if expected else 'Model checking completed. No error has been found.')
            run(label, tlc + ['-config', '/proof/' + path, '/proof/Participation.tla'],
                expected, marker)
        for mutant in ('ForgedInvitation', 'ForgedGuest', 'ForgedOwner',
                       'ForgedApproval', 'ForgedRevocation', 'ReplayInvitation',
                       'ImplicitReinvite', 'WrongApprovalRequest', 'PublicApproval',
                       'ReuseApproval', 'StartRevoked', 'StaleExecution',
                       'HistoricalRecipients', 'MutableRetry', 'OmissionRevokes',
                       'WrongRevocationScope', 'WrongDeliveryScope', 'ForgedException',
                       'WrongExceptionScope', 'StaleExceptionToken', 'StaleException', 'ExceptionApproves'):
            cfg = participation.replace('Mutation = "None"', f'Mutation = "{mutant}"')
            if mutant in ('WrongRevocationScope', 'WrongDeliveryScope', 'WrongExceptionScope'):
                cfg = cfg.replace('Scopes = {s1}', 'Scopes = {s1, s2}')
                cfg = cfg.replace('GuestMessages = {g1, g2}', 'GuestMessages = {g1}')
            workflow('participation-' + mutant, cfg)
        for witness in ('NoRepeatedAnswers', 'NoPrivateContinuation', 'NoReinvitation', 'NoUnverifiedAnswers'):
            workflow('participation-' + witness, participation, witness)
        workflow('participation', participation)
        for mutant in ('KnownOnly', 'StaleDecision', 'ReplayInvitation', 'DeleteUnowned'):
            (snapshot / (mutant + '.cfg')).write_text(config.replace(mutant + ' = FALSE', mutant + ' = TRUE'))
            run(mutant, tlc + ['-config', '/proof/' + mutant + '.cfg', '/proof/Guest.tla'], 12, 'Invariant Safety is violated')
        run('tlc', tlc + ['-config', '/proof/Guest.cfg', '/proof/Guest.tla'], marker='Model checking completed. No error has been found.')
        if args.expand:
            scoped = participation.replace('Scopes = {s1}', 'Scopes = {s1, s2}')
            scoped = scoped.replace('GuestMessages = {g1, g2}', 'GuestMessages = {g1}')
            # Scope isolation needs one invitation identity; replay with multiple
            # identities is covered by the default and mutation configurations.
            scoped = scoped.replace('Evidence = {e1, e2}', 'Evidence = {e1}')
            workflow('participation-scopes', scoped)
            (snapshot / 'Expanded.cfg').write_text(config.replace('Threads = {t1}', 'Threads = {t1, t2}'))
            run('tlc-expanded', tlc + ['-config', '/proof/Expanded.cfg', '/proof/Guest.tla'], marker='Model checking completed. No error has been found.')
            matrix = config.replace('Sparse = FALSE', 'Sparse = TRUE')
            for dimension, first, second in [('Pairs', 'p1', 'p2'), ('Inboxes', 'i1', 'i2'), ('Guests', 'g1', 'g2'), ('Threads', 't1', 't2')]:
                matrix = matrix.replace(f'{dimension} = {{{first}}}', f'{dimension} = {{{first}, {second}}}')
            (snapshot / 'Matrix.cfg').write_text(matrix)
            run('tlc-matrix', tlc + ['-config', '/proof/Matrix.cfg', '/proof/Guest.tla'], marker='Model checking completed. No error has been found.')
        # Include generated baseline configurations as well as checked sources.
        for path in snapshot.glob('*.cfg'):
            shutil.copyfile(path, output / path.name)
        record_artifacts()



if __name__ == '__main__':
    main()
