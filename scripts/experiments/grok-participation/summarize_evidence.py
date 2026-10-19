#!/usr/bin/env python3
"""Export a fixed allowlist from owned coordinator reports, never raw metadata."""
import argparse
import json
from pathlib import Path


NAMES = ('direct-report.json', 'rejected-report.json', 'liveness-normal-report.json', 'monitor-summary.json',
         'monitor-tool-busy-report.json', 'monitor-random-tail-report.json',
         'monitor-lifetime-report.json', 'monitor-environment-report.json',
         'monitor-native-normal-exit.json', 'monitor-native-resume.json',
         'monitor-native-compaction.json', 'monitor-native-crash.json', 'home-probe-report.json',
         'monitor-lifetime-supplement.json')

def summarize(records):
    if 'monitor-payload-report.json' in records:
        raise ValueError('invalidated reused-tail payload evidence is not exportable')
    direct = records['direct-report.json']
    rejected = records['rejected-report.json']
    live = records['liveness-normal-report.json']
    monitor = records['monitor-summary.json']

    def boolean(value):
        if type(value) is not bool:
            raise ValueError('expected a measured boolean')
        return value

    def enum(value, allowed):
        if value not in allowed:
            raise ValueError('unexpected protocol enum; inspect privately')
        return value

    def result(record):
        return record['direct_result']['result']

    def updates(record, kind):
        return [m['params'] for m in record['messages']
                if m.get('method') == 'session/update'
                and m.get('params', {}).get('update', {}).get('sessionUpdate') == kind]

    dr, rr = result(direct), result(rejected)
    dm, rm = dr['_meta'], rr['_meta']
    busy = records['monitor-tool-busy-report.json']
    tail = records['monitor-random-tail-report.json']
    # This exporter accepts the measured negative only. A future retrieval pass
    # needs its own completeness proof, not a reused/inferred tail visibility flag.
    if tail.get('status') != 'fail' or len(tail.get('cases', [])) != 1:
        raise ValueError('payload success requires independent retrieval evidence')
    case = tail['cases'][0]
    if (case.get('body_bytes') != 2048 or case.get('kind') != 'ascii'
            or case.get('tail_reply_observed') is not False):
        raise ValueError('unverified payload result is not exportable')
    def seconds(value):
        if type(value) not in (int, float) or not 0 <= value <= 3600:
            raise ValueError('unexpected measurement duration')
        return value
    calls = rm['usage']['modelCalls']
    if type(calls) is not int or not 0 <= calls <= 100:
        raise ValueError('unexpected modelCalls; inspect privately')
    lifetime = records['monitor-lifetime-report.json']
    lifetime_status = enum(lifetime['status'], ('pass', 'fail'))
    hold = lifetime['hold_seconds']
    if type(hold) is not int or not 2100 <= hold <= 3600:
        raise ValueError('completed long-idle evidence requires at least 2100 seconds')
    checks = {k: boolean(lifetime['checks'][k]) for k in (
        'auto_initial_wake', 'control_initial_wake', 'auto_quiet', 'control_quiet', 'long_idle_wake')}
    if (lifetime_status == 'pass') != all(checks.values()):
        raise ValueError('long-idle status contradicts required observations')
    supplement = records['monitor-lifetime-supplement.json']
    supplemental_checks = {k: boolean(supplement[k]) for k in (
        'raw_screen_failure_retained', 'both_quiet_during_hold', 'ingress_after_hold',
        'turn_native_id_matches', 'turn_completed', 'assistant_export_exact_nonce',
        'original_tui_after_scroll_nonce_visible', 'only_scroll_keys_after_observation_window',
        'control_export_has_no_long_nonce')}
    started = seconds(supplement['turn_started_seconds'])
    completed = seconds(supplement['turn_completed_seconds'])
    if (supplement['status'] != 'pass' or supplement['raw_screen_scorer_status'] != 'fail'
            or lifetime_status != 'fail' or checks['long_idle_wake'] is not False
            or not all(v for k, v in checks.items() if k != 'long_idle_wake')
            or type(supplement['hold_seconds']) is not int or supplement['hold_seconds'] != hold
            or not all(supplemental_checks.values()) or not 0 <= started <= completed <= 120):
        raise ValueError('supplemental pass requires all correlated native and scroll-only witnesses')
    environment = records['monitor-environment-report.json']
    names = environment['names']
    if names != ['GROK_AGENT']:
        raise ValueError('unexpected monitor environment; inspect privately')
    ancestry = [a['kernel_name'] for a in environment['ancestors']]
    if ancestry != ['Python', 'bash', 'grok-macos-aarch', 'tmux']:
        raise ValueError('unexpected monitor ancestry; inspect privately')
    normal = records['monitor-native-normal-exit.json']
    resume = records['monitor-native-resume.json']
    compact = records['monitor-native-compaction.json']
    crash = records['monitor-native-crash.json']
    home = records['home-probe-report.json']
    return {
        'schema': 3,
        'build': '1.0.62 (1474f9cbdd91) [stable]',
        'scope': 'owned synthetic sessions; allowlisted assertions only',
        'sources': list(NAMES),
        'production_package_acceptance': 'not_established',
        'monitor_long_idle': {
            'status': lifetime_status, 'hold_seconds': hold, 'checks': checks,
            'completion_seconds': seconds(lifetime['completion_seconds']),
            'scope': 'raw screen scorer; viewport false-negative retained',
        },
        'monitor_long_idle_supplement': {
            'status': 'pass', 'scope': 'native-only; supplemental evidence, not rerun',
            'hold_seconds': hold, 'checks': supplemental_checks,
            'turn_started_seconds': started, 'turn_completed_seconds': completed,
        },
        'monitor_environment': {
            'home_equals_hook': boolean(environment['monitor_home_equals_hook']),
            'native_id_present': boolean(environment['grok_session_id_present']),
            'grok_variable_names': ['GROK_AGENT'], 'ancestor_kernel_names': ancestry,
        },
        'native_lifecycle': {
            'scope': 'native child only; not production watcher acceptance',
            'normal_exit': {'monitor_exited': boolean(normal['monitor_exited']),
                            'elapsed_seconds': seconds(normal['elapsed_seconds'])},
            'resume_explicit_rearm': {k: boolean(resume[k]) for k in (
                'same_native_id', 'generated_nonce_visible', 'monitor_count_text_one')},
            'compaction': {**{k: boolean(compact[k]) for k in (
                'compaction_completed', 'generated_nonce_visible', 'monitor_count_text_one')},
                'elapsed_seconds': seconds(compact['elapsed_seconds'])},
            'crash': {'monitor_exited_within_10s': boolean(crash['monitor_exited_within_10s']),
                      'elapsed_seconds': seconds(crash['elapsed_seconds']),
                      'owned_orphan_cleanup_sent': boolean(crash['owned_orphan_cleanup_sent'])},
        },
        'home_split': {k: boolean(home[k]) for k in (
            'normal_auth_model_reply', 'shell_home_matches_scratch',
            'shell_native_id_matches', 'grok_home_matches_normal')},
        'direct_transport': {
            'live_info_present': bool(direct['live_info']['result']['result'].get('sessionId')),
            'missing_info_empty': direct['missing_info']['result']['result'] == {},
            'stop_reason': enum(dr['stopReason'], ('end_turn', 'cancelled')),
            'exact_generated_nonce': direct['direct_text'].strip() == 'DIRECT_LIVE_120',
            'response_ids_agree': bool(dm.get('promptId')) and dm.get('requestId') == dm['promptId'],
            'agent_chunks_correlated': bool(updates(direct, 'agent_message_chunk')) and all(
                p.get('_meta', {}).get('promptId') == dm['promptId']
                for p in updates(direct, 'agent_message_chunk')),
        },
        'rejected_prompt': {
            'stop_reason': enum(rr['stopReason'], ('end_turn', 'cancelled')),
            'cancellation_category': enum(rm.get('cancellationCategory'), ('HookDenied',)),
            'model_calls': calls,
            'usage_matches_preceding_turn': rm['usage'] == dm['usage'],
            'user_echo_present': bool(updates(rejected, 'user_message_chunk')),
            'agent_content_present': bool(updates(rejected, 'agent_message_chunk')),
            'generated_text_empty': rejected['direct_text'] == '',
        },
        'normal_exit': {
            'before_present': boolean(live['before_present']),
            'after_exit_present': boolean(live['after_normal_exit_present']),
            'ghost_prompt_rpc_succeeded': live['closed_prompt_error_code'] is None,
            'ghost_stop_reason': enum(live['closed_prompt_stop'], ('end_turn', 'cancelled')),
            'ghost_reply_observed': boolean(live['ghost_reply']),
        },
        'monitor_tool_busy': {
            'tool_in_flight': boolean(busy['tool_in_flight']),
            'tool_completed': boolean(busy['tool_completed']),
            'nonce_observed': boolean(busy['nonce_observed']),
            'completion_seconds': seconds(busy['completion_seconds']),
            'status': enum(busy['status'], ('pass', 'fail')),
        },
        'direct_monitor_payload': {
            'previous_reused_tail_cases': 'invalidated',
            'full_body_delivery_established': False,
            'fresh_random_tail_2048': {
                'status': 'fail', 'body_bytes': 2048, 'tail_reply_observed': False,
                'completion_seconds': seconds(case['completion_seconds']),
            },
        },
        'monitor_source_reference': {
            'git_commit': '2bdd1d6a6369de0e8c68132ea4539e9abd9e14a8',
            'matches_installed_binary': False,
            'line_truncation_constant': 500, 'batch_truncation_constant': 3000,
            'rate_bucket_capacity': 10, 'refill_interval_ms': 2000,
            'basis': 'pinned public source; not installed-runtime limit measurement',
        },
        'nonleader_monitor': {k: boolean(monitor[k]) for k in (
            'hook_parent_equals_tui', 'socket_env_present', 'native_identity_matches', 'auto_nonce_visible')},
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--private-dir', required=True, type=Path)
    parser.add_argument('--output', required=True, type=Path)
    args = parser.parse_args()
    records = {name: json.loads((args.private_dir / name).read_text()) for name in NAMES}
    args.output.write_text(json.dumps(summarize(records), indent=2) + '\n')


if __name__ == '__main__':
    main()
