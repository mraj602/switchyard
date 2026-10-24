import math, subprocess, sys, os
OUT = sys.argv[1]; WORK = sys.argv[2]
BG='#17191B'; FG='#ECEEEF'; DIM='#9AA1A8'; NEEDS='#FF9F0A'; TRACK='#2A2E32'; ACC='#E8B04B'; ON='#4FC38A'; OFF='#F0877D'; INK='#17191B'
MARK=f'<svg viewBox="1 0 20 20" width="14" height="14" style="margin-right:5px"><g fill="none" stroke="{ACC}" stroke-width="2.2" stroke-linejoin="miter" stroke-linecap="butt"><path d="M3 18 L11 4 L19 18"/><path d="M7 18 L11 11 L15 18"/></g></svg>'
def brand(t): return f'<div class="row" style="align-items:center">{MARK}<b style="font-size:11px">{t}</b></div>'
def toggle(sel):
    seg=lambda w:f'<span style="padding:2px 7px;border-radius:6px;font-size:10px;font-weight:bold;background:{ACC if w==sel else TRACK};color:{INK if w==sel else DIM}">{w}</span>'
    return f'<div class="row" style="border-radius:7px;background:{TRACK}">{seg("5 h")}{seg("7 d")}</div>'
def ring(p,c,size,stroke):
    r=(size-stroke)/2;cc=size/2;a=p/100*2*math.pi
    x=cc+r*math.sin(a);y=cc-r*math.cos(a)
    arc=f'<path d="M {cc} {cc-r} A {r} {r} 0 {1 if p>50 else 0} 1 {x:.2f} {y:.2f}" fill="none" stroke="{c}" stroke-width="{stroke}" stroke-linecap="round"/>' if 0<p<100 else ''
    return f'<svg width="{size}" height="{size}" viewBox="0 0 {size} {size}"><circle cx="{cc}" cy="{cc}" r="{r}" fill="none" stroke="{TRACK}" stroke-width="{stroke}"/>{arc}</svg>'
def card(w,h,body,pad=12): return (w,h,f'<div class="card" style="width:{w}px;height:{h}px;padding:{pad}px">{body}</div>')
def limits():
    rows=[('claude-1','#8B6CF6',62,'resets 15:48'),('claude-2','#4FC3B5',23,'resets 17:10'),('claude-3','#FF8A65',88,'resets 14:05'),('claude-4','#E8B04B',0,'not started')]
    b=f'<div class="row" style="justify-content:space-between;align-items:center">{brand("Watchyard · Limits")}<span style="font-size:9px;color:{DIM}">updated 2 min ago</span>{toggle("5 h")}</div>'
    for n,c,p,t in rows:
        b+=f'<div class="row" style="align-items:center;margin-top:6px"><b style="width:72px;font-size:12px;color:{c}">{n}</b><div class="row" style="flex:1;height:6px;border-radius:3px;background:{TRACK};margin:0 6px"><div style="width:{p}%;border-radius:3px;background:{c}"></div></div><b style="width:34px;font-size:12px">{p}%</b><span style="width:72px;font-size:10px;color:{DIM}">{t}</span></div>'
    return card(300,150,b)
def sessions():
    cell=lambda n,l,c:f'<div style="flex:1;text-align:center"><div style="font-size:28px;font-weight:bold;color:{c}">{n}</div><div style="font-size:11px;color:{DIM}">{l}</div></div>'
    b=f'<div class="row" style="justify-content:space-between;align-items:center">{brand("Watchyard · Sessions")}<span style="font-size:9px;color:{DIM}">updated 2 min ago</span></div><div class="row" style="margin-top:6px">{cell(5,"Running",ON)}{cell(2,"Needs input",NEEDS)}{cell(8,"Done",FG)}</div><div style="margin-top:8px;padding:6px;border-radius:8px;background:#3A2A0E;color:{NEEDS};font-weight:bold;font-size:12px;white-space:nowrap;overflow:hidden;text-overflow:ellipsis">Needs you: api · asked</div>'
    return card(240,124,b)
def needs():
    rows=[('claude-2','#4FC3B5','api','asked','4 min'),('claude-4','#FF8A65','etl','needs permission','9 min'),('claude-1','#8B6CF6','web','asked','21 min')]
    b=f'<div class="row" style="justify-content:space-between;align-items:center">{brand("Watchyard · Needs you")}<b style="font-size:11px;color:{NEEDS}">3 waiting</b><span style="font-size:9px;color:{DIM}">updated 2 min ago</span></div>'
    for n,c,p,s,a in rows:
        b+=f'<div class="row" style="align-items:center;margin-top:8px"><div style="width:8px;height:8px;border-radius:4px;background:{c};margin-right:8px"></div><b style="flex:1;font-size:12px;white-space:nowrap;overflow:hidden;text-overflow:ellipsis">{n} · {p} · {s}</b><span style="font-size:11px;color:{DIM};margin-left:6px">{a}</span></div>'
    return card(300,128,b)
def ringw():
    b=f'<div style="display:flex;flex-direction:column;align-items:center;height:100%"><div style="width:100%">{brand("Watchyard")}</div><div class="row" style="width:100%;justify-content:space-between;align-items:center;margin-top:3px"><b style="font-size:11px;color:#4FC3B5">claude-2</b>{toggle("5 h")}</div><div style="position:relative;width:58px;height:58px;margin-top:4px">{ring(23,"#4FC3B5",58,7)}<div style="position:absolute;left:0;top:0;width:58px;height:58px;display:flex;align-items:center;justify-content:center;font-size:14px;font-weight:bold">23 %</div></div><div style="font-size:10px;color:{DIM};margin-top:2px">resets 15:48</div></div>'
    return card(130,130,b,8)
def nextr():
    b=f'{brand("Watchyard")}<div class="row" style="align-items:baseline;margin-top:2px"><b style="font-size:20px">15:48</b><span style="font-size:10px;color:{DIM};white-space:pre">  claude-2 · 5 h</span></div>'
    return card(130,56,b,8)
def machines():
    ms=[('gpu-box',True,'2 live'),('laptop',True,'1 live'),('vps',False,'silent 1 h')]
    b=f'<div class="row" style="justify-content:space-between;align-items:center">{brand("Watchyard")}<b style="font-size:11px">Machines · 2 of 3 online</b><span style="font-size:9px;color:{DIM}">updated 2 min ago</span></div><div class="row" style="margin-top:6px">'
    for n,o,t in ms:
        b+=f'<div class="row" style="flex:1;align-items:center"><div style="width:7px;height:7px;border-radius:4px;background:{ON if o else OFF};margin-right:5px"></div><b style="font-size:12px">{n}</b><span style="font-size:10px;color:{DIM if o else OFF};white-space:pre"> {t}</span></div>'
    b+='</div>'
    return card(300,64,b)
W={'wy-limits':limits(),'wy-sessions':sessions(),'wy-needs':needs(),'wy-ring':ringw(),'wy-next-reset':nextr(),'wy-machines':machines()}
CSS=f'html,body{{margin:0;background:transparent}}body{{font-family:Roboto,"DejaVu Sans",sans-serif;color:{FG}}}.row{{display:flex;flex-direction:row}}.card{{box-sizing:border-box;background:{BG};border-radius:16px;overflow:hidden}}b{{font-weight:bold}}'
for name,(w,h,html) in W.items():
    p=os.path.join(WORK,name+'.html')
    open(p,'w').write(f'<!doctype html><meta charset=utf-8><style>{CSS}</style>{html}')
    subprocess.run(['dbus-run-session','--','/usr/bin/google-chrome','--headless=new','--no-sandbox','--disable-gpu','--hide-scrollbars','--default-background-color=00000000','--force-device-scale-factor=2',f'--window-size={w},{h}',f'--screenshot={os.path.join(OUT,name+".png")}','file://'+p],check=True,capture_output=True)
    print(name,w,h)
