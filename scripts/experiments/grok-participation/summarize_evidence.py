#!/usr/bin/env python3
"""Export a fixed allowlist from owned coordinator reports, never raw metadata."""
import argparse
import json
from pathlib import Path


NAMES = ('direct-report.json', 'rejected-report.json', 'liveness-normal-report.json', 'monitor-summary.json',
         'monitor-tool-busy-report.json', 'monitor-random-tail-report.json')

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
    return {
        'schema': 2,
        'build': '1.0.62 (1474f9cbdd91) [stable]',
        'scope': 'owned synthetic sessions; allowlisted assertions only',
        'sources': list(NAMES),
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
