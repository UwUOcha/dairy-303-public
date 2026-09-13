// Run with: node --test tests/admin-ui.test.cjs
const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');

function panel(fetch) {
  const nodes = new Map();
  const node = () => ({ children: [], attrs: {}, style: {}, textContent: '',
    classList: {add(){}, remove(){}},
    setAttribute(k,v){this.attrs[k]=v;},
    getBoundingClientRect(){return {width:800};},
    replaceChildren(...children){this.children=children;},
    appendChild(child){this.children.push(child);},
  });
  const context = vm.createContext({fetch, AbortController, setTimeout, clearTimeout,
    document: {body:{dataset:{}}, getElementById(id){if(!nodes.has(id))nodes.set(id,node());return nodes.get(id);}, createElement:node,
      createTextNode:text=>({textContent:String(text),children:[]})},
  });
  const source = fs.readFileSync('internal/admin/assets/app.js','utf8').replace(/start\(\);\s*$/, '');
  vm.runInContext(source, context);
  return {run: code => vm.runInContext(code,context), nodes};
}
const response = samples => ({ok:true,json:async()=>({samples})});

test('late history response cannot overwrite the chosen period', async()=>{
  const pending=[];
  const p=panel(()=>new Promise(resolve=>pending.push(resolve)));
  const first=p.run('loadHistory()');
  p.run("state.range='7d'");
  const second=p.run('loadHistory()');
  pending[1](response([{at:7}])); await second;
  pending[0](response([{at:1}])); await first;
  assert.equal(p.run('state.samples[0].at'),7);
  assert.equal(p.nodes.get('chart-host').attrs['aria-busy'],'false');
});

test('failed history has a visible retry hint and can recover', async()=>{
  let fail=true;
  const p=panel(async()=> fail ? {ok:false,status:503} : response([]));
  await p.run('loadHistory()');
  assert.match(p.nodes.get('chart-host').children[0].textContent,/Не удалось загрузить историю/);
  fail=false; await p.run('loadHistory()');
  assert.equal(p.run('state.historyStatus'),'');
});

test('UTC offset zero is respected and stats requests do not overlap', async()=>{
  let resolve, calls=0;
  const p=panel(()=>{calls++; return new Promise(r=>resolve=r);});
  p.run('render = () => {}; drawHostChart = () => {}');
  const first=p.run('loadStats()');
  await p.run('loadStats()');
  assert.equal(calls,1);
  resolve({ok:true,json:async()=>({tz_offset:0})}); await first;
  assert.equal(p.run('state.tz'),0);
  assert.equal(p.run('state.statsPending'),false);
});

test('multi-day chart labels distinguish dates',()=>{
  const p=panel();
  p.run("state.range='7d'");
  assert.notEqual(p.run('chartTime(1788566400)'),p.run('chartTime(1788652800)'));
});

// Ушедшие показываются одной строкой в «кто приходит», а не отдельным блоком:
// знать это полезно, натыкаться на это каждый раз — нет.
test('blocked users are one line plus the groups they left', () => {
  const p = panel();
  const users = {
    configured: 100, probed: 100, blocked: 3,
    notify: {notify_on: 90, morning: 80, evening: 10, changes: 70, empty_days: 5},
    blocked_groups: [{name: 'ГР-12', count: 2}, {name: 'ГР-11', count: 1}],
  };
  p.run('view = ' + JSON.stringify({rasp: {stats: {users}}}) + '; renderReach(view)');

  const rows = p.nodes.get('reach').children;
  const line = rows[rows.length - 1];
  assert.equal(line.children[0].textContent, 'заблокировали бота');
  assert.equal(line.children[1].children[0].textContent, '3');
  assert.equal(line.children[1].children.length, 1, 'обход прошёл базу — пояснения быть не должно');

  const gone = p.nodes.get('gone');
  assert.equal(gone.hidden, false);
  assert.match(gone.children[1].textContent, /ГР-12/);
  assert.match(gone.children[1].textContent, /ГР-11/);
});

// Пока обход не прошёл базу целиком, ноль ушедших значит «ещё не спрашивали».
test('an unfinished sweep says so instead of claiming nobody left', () => {
  const p = panel();
  const users = {
    configured: 100, probed: 40, blocked: 0,
    notify: {notify_on: 90, morning: 80, evening: 10, changes: 70, empty_days: 5},
  };
  p.run('view = ' + JSON.stringify({rasp: {stats: {users}}}) + '; renderReach(view)');

  const rows = p.nodes.get('reach').children;
  const note = rows[rows.length - 1].children[1].children[1];
  assert.match(note.textContent, /проверено 40 из 100/);
  assert.equal(p.nodes.get('gone').hidden, true, 'уходить некому — строки быть не должно');
});

test('website stats distinguish accounts, devices, zero and unavailable data', () => {
  const p = panel();
  p.run('renderWeb({rasp:{stats:{web:{signed_in:2,sessions:3,allowed:12,telegram:1,vk:1}}}})');
  let cards = p.nodes.get('web-figures').children;
  assert.deepEqual(cards.map(c=>c.children[0].textContent), ['2','3','1','1']);
  assert.match(cards[0].children[2].textContent, /12 аккаунтов/);
  p.run('renderWeb({rasp:{stats:{web:{signed_in:0,sessions:0,allowed:12,telegram:0,vk:0}}}})');
  assert.equal(p.nodes.get('web-figures').children[0].children[0].textContent,'0');
  p.run('renderWeb({rasp:null})');
  assert.match(p.nodes.get('web-figures').children[0].textContent,/недоступна/);
  p.run('renderWeb({rasp:{stats:{}}})');
  assert.match(p.nodes.get('web-figures').children[0].textContent,/недоступна/);
});
