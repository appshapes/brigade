import json,sys
for path in sys.argv[1:]:
    print("=================", path)
    for line in open(path):
        try: ev=json.loads(line)
        except Exception: continue
        t=ev.get('type'); st=ev.get('subtype')
        if t=='system' and st=='init':
            print('INIT plugins=',[p.get('name') for p in ev.get('plugins',[])],'errors=',ev.get('plugin_errors'),'mode=',ev.get('permissionMode'),'skills=',[s for s in ev.get('slash_commands',[]) if 'brigade' in s])
        elif t=='system' and st in('hook_started','hook_response'):
            if st=='hook_response': print('HOOK', ev.get('hook_name'), 'exit=',ev.get('exit_code'), 'out=', (ev.get('output') or '')[:400].replace('\n',' | '), 'err=', (ev.get('stderr') or '')[:300].replace('\n',' | '))
        elif t=='system':
            print('SYSTEM', st, json.dumps({k:v for k,v in ev.items() if k not in('type','session_id','uuid')})[:400])
        elif t=='assistant':
            for c in ev['message']['content']:
                if c['type']=='tool_use': print('TOOL_USE', c['name'], json.dumps(c['input'])[:700])
                elif c['type']=='text': print('ASSISTANT_TEXT', c['text'][:1800])
        elif t=='user':
            for c in ev['message']['content']:
                if isinstance(c,dict) and c.get('type')=='tool_result':
                    cont=c.get('content')
                    if isinstance(cont,list): cont=' '.join(x.get('text','') for x in cont if isinstance(x,dict))
                    print('TOOL_RESULT is_error=',c.get('is_error'), str(cont)[:1600])
        elif t=='result':
            print('RESULT', st, 'duration_ms=',ev.get('duration_ms'), 'turns=',ev.get('num_turns'), 'is_error=',ev.get('is_error'), 'permission_denials=', json.dumps(ev.get('permission_denials'))[:800])
