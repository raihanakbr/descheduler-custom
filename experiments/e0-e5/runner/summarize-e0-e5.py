#!/usr/bin/env python3
import json, pathlib, re, sys, datetime
result=pathlib.Path(sys.argv[1]); group=sys.argv[2]; ns=sys.argv[3]

def pod_count(path):
    try: return len(json.loads(path.read_text()).get('items', []))
    except Exception: return 0

def pending(path):
    try:
        items=json.loads(path.read_text()).get('items', [])
        probes=[p for p in items if p.get('metadata',{}).get('labels',{}).get('app')=='probe']
        return sum(1 for p in probes if p.get('status',{}).get('phase')=='Pending'), sum(1 for p in probes if p.get('spec',{}).get('nodeName'))
    except Exception: return 0,0

logs='\n'.join(p.read_text(errors='ignore') for p in result.glob('descheduler-*.log'))
evicted=len(re.findall(r'Evicted pod', logs))
print(f'# E0-E5 summary: {group}')
print(f'- namespace: `{ns}`')
print(f'- generated_at_utc: `{datetime.datetime.utcnow().isoformat()}Z`')
print(f'- pods_evicted: {evicted}')
for p in sorted(result.glob('pods-*.json')):
    pend, sched = pending(p)
    print(f'- {p.stem}: pods={pod_count(p)}, probe_scheduled={sched}, probe_pending={pend}')
print('\nManual follow-up: compute request-vs-actual-vs-RII alignment and avg/max abs RII from node snapshots/top output.')
