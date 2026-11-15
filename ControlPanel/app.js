'use strict';

const $ = (id) => document.getElementById(id);
let categoryNames = ['general', 'coding', 'reasoning', 'writing', 'vision', 'audio', 'embedding', 'uncategorized'];
const state = { models: [], catalog: [], servers: [], hardware: null, runtimeCapabilities: {}, selectedModel: '', selectedCategory: 'general', profiles: {}, busy: false, stoppedGrace: new Map(), currentView: 'home' };
const fields = {
  port: 'portInput', contextSize: 'contextInput', gpuBackend: 'backendSelect', gpuLayers: 'gpuLayersInput',
  cpuThreads: 'cpuThreadsInput', threadsBatch: 'threadsBatchInput', batchSize: 'batchSizeInput', ubatchSize: 'ubatchSizeInput',
  parallel: 'parallelInput', flashAttention: 'flashAttentionSelect', noMmap: 'noMmapInput', mlock: 'mlockInput',
  cacheTypeK: 'cacheTypeKSelect', cacheTypeV: 'cacheTypeVSelect', imageMinTokens: 'imageMinTokensInput', imageMaxTokens: 'imageMaxTokensInput', customArgs: 'customArgsInput',
  temperature: 'temperatureInput', topP: 'topPInput', topK: 'topKInput', minP: 'minPInput', repeatPenalty: 'repeatPenaltyInput', maxTokens: 'maxTokensInput'
};
let toastTimer;

function toast(message) {
  const el = $('toast'); el.textContent = message; el.classList.add('show');
  clearTimeout(toastTimer); toastTimer = setTimeout(() => el.classList.remove('show'), 3200);
}
function setStatus(message, kind = '') { const el = $('launchStatus'); el.textContent = message; el.className = `status-line ${kind}`.trim(); }
function bytesLabel(n) { if (!Number.isFinite(Number(n))) return 'Unknown'; const units = ['B','KB','MB','GB','TB']; let v=Number(n), i=0; while(v>=1024 && i<units.length-1){v/=1024;i++;} return `${v.toFixed(i>1?2:0)} ${units[i]}`; }
function gb(n) { return Number(n || 0) / (1024 ** 3); }
function esc(s) { return String(s ?? ''); }
async function api(path, options = {}) {
  const response = await fetch(path, { ...options, headers: { ...(options.body ? {'Content-Type':'application/json'} : {}), ...(options.headers || {}) } });
  const type = response.headers.get('content-type') || '';
  const data = type.includes('application/json') ? await response.json() : await response.text();
  if (!response.ok) throw new Error(typeof data === 'object' ? (data.error || `Request failed (${response.status})`) : `Request failed (${response.status}): ${data}`);
  return data;
}
function setConnection(ok, text) { $('connectionDot').className = `dot ${ok ? 'good' : 'bad'}`; $('connectionText').textContent = text; }

async function init() {
  try {
    const health = await api('/api/health');
    setConnection(true, 'Controller connected');
    state.runtimeCapabilities=health.runtimeCapabilities||{}; applyRuntimeCapabilities();
    $('runtimeInfo').textContent = `Controller ${health.version} · ${health.platform}/${health.architecture} · Runtime ${health.runtimeVersion||'unknown'} · ${health.runtime}`;
    await Promise.all([loadModels(), loadCatalog(), refreshServers(), loadAppConfig(), loadSetupGuide()]);
  } catch (err) { setConnection(false, `Controller unavailable: ${err.message}`); }
  bindEvents();
}
function applyRuntimeCapabilities() {
  const mapping=[['cpuThreadsInput','--threads'],['threadsBatchInput','--threads-batch'],['batchSizeInput','--batch-size'],['ubatchSizeInput','--ubatch-size'],['parallelInput','--parallel'],['flashAttentionSelect','--flash-attn'],['noMmapInput','--no-mmap'],['mlockInput','--mlock'],['cacheTypeKSelect','--cache-type-k'],['cacheTypeVSelect','--cache-type-v'],['imageMinTokensInput','--image-min-tokens'],['imageMaxTokensInput','--image-max-tokens']];
  for(const [id,flag] of mapping){const el=$(id);if(!el)continue;const supported=!!state.runtimeCapabilities[flag];el.disabled=!supported;el.title=supported?'Supported by the detected runtime':`This runtime does not advertise ${flag}`;}
}
function bindEvents() {
  // Some optional controls may be absent when an older/newer HTML patch is mixed in.
  // Bind only elements that exist so one missing control cannot break server management.
  const bind = (id, event, handler) => {
    const el = $(id);
    if (!el) {
      console.warn(`PortableAI UI: optional element #${id} is missing; skipped ${event} handler.`);
      return;
    }
    el.addEventListener(event, handler);
  };
  showView(state.currentView);
  bind('detectModelsBtn', 'click', detectModels);
  bind('sidebarToggle','click',()=>{const collapsed=$('sidebar').classList.toggle('collapsed');document.body.classList.toggle('sidebar-collapsed',collapsed);});
  document.querySelectorAll('.nav-item').forEach(b=>b.addEventListener('click',()=>showView(b.dataset.view)));
  bind('refreshRunningBtn','click',refreshServers);
  bind('refreshCatalogBtn','click',loadCatalog);
  bind('hardwareCollapseBtn','click',()=>{const d=$('hardwareDetails'),b=$('hardwareCollapseBtn');const collapsed=!d.classList.contains('hidden');d.classList.toggle('hidden',collapsed);b.textContent=collapsed?'Expand details ↑':'Collapse details ↓';b.setAttribute('aria-expanded',String(!collapsed));});
  bind('detectHardwareBtn', 'click', detectHardware);
  bind('categorySelect', 'change', () => { state.selectedCategory=$('categorySelect').value; renderModelSelect(); });
  bind('modelSelect', 'change', async () => { state.selectedModel=$('modelSelect').value; renderProjectorSelect(); renderCatalogSelect(); fillMetadataFields(); renderModelDetails(); renderCategoryChecks(); await loadProfile(state.selectedModel); });
  bind('saveMetadataBtn', 'click', saveMetadata);
  bind('saveSettingsBtn', 'click', saveProfile);
  bind('startServerBtn', 'click', startServer);
  bind('refreshServersBtn', 'click', refreshServers);
  bind('closeLogBtn', 'click', () => { const dialog=$('logDialog'); if (dialog && typeof dialog.close === 'function') dialog.close(); });
  bind('exitControllerBtn', 'click', shutdownController);
  bind('addCategoryBtn', 'click', addCustomCategory);
  bind('contextInput', 'input', renderModelDetails);
  bind('backendSelect', 'change', renderModelDetails);
  bind('serverModeSelect', 'change', saveAppConfig);
  bind('projectorSelect', 'change', renderModelDetails);
}

async function shutdownController() {
  if(!confirm('Exit PortableAI and stop every model server owned by this controller? Unrelated processes will not be touched.'))return;
  try { await api('/api/shutdown',{method:'POST',body:'{}'}); setConnection(false,'Controller shutting down'); toast('Owned model servers are being stopped. You can close this tab.'); }
  catch(err) { toast(`Could not request shutdown: ${err.message}`); }
}
async function loadAppConfig() {
  try { const c=await api('/api/config'); $('serverModeSelect').value=c.serverMode || 'single'; }
  catch(err) { toast(`Could not load app configuration: ${err.message}`); }
}
async function saveAppConfig() {
  try { await api('/api/config',{method:'POST',body:JSON.stringify({serverMode:$('serverModeSelect').value})}); toast('Server mode saved to this drive.'); }
  catch(err) { toast(`Could not save server mode: ${err.message}`); }
}
async function loadModels() {
  const result = await api('/api/models'); state.models = result.models || []; updateCategoryNames(); renderModelSelect();
  $('modelCount').textContent = `${state.models.filter(m => m.role !== 'projector').length} models · ${state.models.filter(m => m.role === 'projector').length} projectors`;
}
async function detectModels() {
  const button = $('detectModelsBtn'); button.disabled = true; button.textContent = 'Scanning…';
  try {
    const result = await api('/api/models/refresh', {method:'POST', body:'{}'});
    state.models = result.models || []; updateCategoryNames(); renderModelSelect();
    $('modelCount').textContent = `${state.models.filter(m => m.role !== 'projector').length} models · ${state.models.filter(m => m.role === 'projector').length} projectors`;
    toast(`Detected ${result.count} GGUF files. ${result.newFiles} new.`);
  } catch (err) { toast(`Model scan failed: ${err.message}`); }
  finally { button.disabled = false; button.textContent = 'Add / Detect Models'; }
}
function updateCategoryNames() {
  const all = new Set(['general','coding','reasoning','writing','vision','audio','embedding','uncategorized']);
  for (const m of state.models) for (const c of m.categories || []) all.add(c);
  categoryNames = [...all].sort((a,b) => { const order=['general','coding','reasoning','writing','vision','audio','embedding','uncategorized']; const ai=order.indexOf(a),bi=order.indexOf(b); if(ai>=0||bi>=0)return (ai<0?999:ai)-(bi<0?999:bi); return a.localeCompare(b); });
  const select=$('categorySelect');const current=state.selectedCategory;select.replaceChildren();for(const c of categoryNames){const o=document.createElement('option');o.value=c;o.textContent=c[0].toUpperCase()+c.slice(1);select.append(o);}if(!categoryNames.includes(current))state.selectedCategory='general';select.value=state.selectedCategory;
}
function addCustomCategory() {
  const value=$('customCategoryInput').value.trim().toLowerCase().replace(/\s+/g,'-');
  if(!/^[a-z0-9][a-z0-9-]{0,31}$/.test(value)){toast('Use 1–32 letters, numbers, and hyphens for a category.');return;}
  if(!categoryNames.includes(value)){categoryNames.push(value);categoryNames.sort();}
  const m=findModel(state.selectedModel);if(!m){toast('Select a model before adding a category.');return;}
  const checks=$('categoryChecks');if(![...checks.querySelectorAll('input')].some(i=>i.value===value)){const label=document.createElement('label');label.className='check';const input=document.createElement('input');input.type='checkbox';input.value=value;input.checked=true;const span=document.createElement('span');span.textContent=value;label.append(input,span);checks.append(label);}else{const input=[...checks.querySelectorAll('input')].find(i=>i.value===value);input.checked=true;}
  $('customCategoryInput').value='';toast(`Category “${value}” will be saved with this model.`);
}
function categoryModels() {
  return state.models.filter(m => m.role !== 'projector' && (m.categories || []).includes(state.selectedCategory));
}
function renderModelSelect() {
  const select = $('modelSelect'); const candidates = categoryModels();
  select.replaceChildren();
  if (!candidates.length) {
    const option = document.createElement('option'); option.value=''; option.textContent='No models tagged in this category'; select.append(option); state.selectedModel='';
  } else {
    for (const m of candidates) { const option=document.createElement('option'); option.value=m.path; option.textContent=`${m.name} — ${m.path}`; select.append(option); }
    if (!candidates.some(m=>m.path===state.selectedModel)) state.selectedModel=candidates[0].path;
    select.value=state.selectedModel;
  }
  renderProjectorSelect(); renderModelDetails(); renderCategoryChecks(); renderCatalogSelect(); fillMetadataFields();
  $('modelTagsEditor').classList.toggle('hidden', !state.selectedModel);
  if (state.selectedModel) loadProfile(state.selectedModel).catch(err=>setStatus(err.message,'error'));
}
function findModel(path) { return state.models.find(m => m.path === path); }
function fitEstimate(model) {
  if (!state.hardware || !model) return {label:'Not assessed', detail:'Detect hardware to estimate resource fit.', kind:'warn'};
  const projectorPath = $('projectorSelect')?.value || model.mmprojPath;
  const sizeGB = gb(model.sizeBytes) + (projectorPath ? gb((findModel(projectorPath)||{}).sizeBytes) : 0);
  const contextGB = Number($('contextInput').value || 8192) * 0.00008;
  const visionExtra = ((model.categories||[]).includes('vision') || /vl|vision|clip/i.test(model.architecture||'')) ? 0.8 : 0.15;
  const overhead = 0.55;
  const requiredGB = sizeGB + contextGB + visionExtra + overhead;
  const backend = $('backendSelect').value;
  const candidates = (state.hardware.gpus || []).filter(g => ['NVIDIA','AMD','Apple'].includes(g.vendor) && (backend==='auto' || backend==='disable' || g.vendor.toLowerCase()===backend));
  const gpu = candidates.sort((a,b)=>((Number(b.vramMB||0)-Number(b.usedVRAMMB||0))-(Number(a.vramMB||0)-Number(a.usedVRAMMB||0))))[0];
  const availableVRAM = gpu ? Math.max(0, Number(gpu.vramMB || 0) - Number(gpu.usedVRAMMB || 0)) / 1024 : 0;
  const availableRAM = gb(state.hardware.availableRamBytes || state.hardware.ramBytes);
  if (backend !== 'auto' && backend !== 'disable' && !gpu) return {label:'High resource risk', detail:`The explicitly selected ${backend} backend was not detected by hardware scan. The controller will not silently switch backends; inspect runtime logs if launch fails.`, kind:'bad'};
  if (backend !== 'disable' && gpu && availableVRAM >= requiredGB * 1.1) return {label:'Fast — likely GPU fit', detail:`Estimated ${requiredGB.toFixed(1)} GB incl. context/overhead; about ${availableVRAM.toFixed(1)} GB VRAM available. Estimate only.`, kind:'good'};
  if (backend !== 'disable' && gpu && availableVRAM >= requiredGB * 0.75) return {label:'Moderate — tight GPU fit', detail:`Estimated ${requiredGB.toFixed(1)} GB vs ${availableVRAM.toFixed(1)} GB available VRAM. Startup may fail or need CPU offload.`, kind:'warn'};
  if (backend !== 'auto' && backend !== 'disable' && gpu) return {label:'High resource risk', detail:`Estimated ${requiredGB.toFixed(1)} GB vs ${availableVRAM.toFixed(1)} GB available VRAM for the explicitly selected backend. Reduce context/model size or choose Auto/CPU only.`, kind:'bad'};
  if (availableRAM >= requiredGB * 1.25) return {label:'Slow — CPU fit likely', detail:`Estimated ${requiredGB.toFixed(1)} GB memory demand vs ${availableRAM.toFixed(1)} GB system RAM available. CPU speed may be slower.`, kind:'warn'};
  return {label:'High resource risk', detail:`Rough estimate ${requiredGB.toFixed(1)} GB vs ${availableVRAM.toFixed(1)} GB VRAM and ${availableRAM.toFixed(1)} GB available RAM. Startup may fail or swap heavily.`, kind:'bad'};
}
function renderModelDetails() {
  const box=$('modelDetails'); const m=findModel(state.selectedModel);
  if (!m) { box.className='details empty-state'; box.textContent='No model selected. Put compatible GGUF files in Models, then click Add / Detect Models.'; $('modelTagsEditor').classList.add('hidden'); return; }
  box.className='details'; box.replaceChildren();
  const size = document.createElement('div'); size.className='tag-list';
  const tags=[`${bytesLabel(m.sizeBytes)}`, m.metadataReadable ? `Architecture: ${m.architecture || 'unknown'}` : 'GGUF metadata unreadable', m.quantization && m.quantization!=='unknown' ? `Quantization: ${m.quantization}` : 'Quantization: filename/metadata unknown', m.licenseVerified ? `Upstream license: ${m.license}` : 'License unverified'];
  for (const t of tags) { const s=document.createElement('span'); s.className=`tag ${t.includes('unverified')?'warn':t.includes('license:')?'good':''}`; s.textContent=t; size.append(s); }
  box.append(size);
  const dl=document.createElement('dl');
  const details=[['Exact filename',m.name],['Relative path',m.path],['Architecture',m.architecture || 'Not detected'],['Architecture status',m.architectureRecognized?'Recognized family':'Unknown / not recognized'],['Runtime status',runtimeStatus(m)],['Launch readiness',launchReadiness(m)],['License',m.licenseVerified?`${m.license} (upstream metadata verified)`:'Unverified; do not treat as approved for redistribution'],['Source',m.sourceUrl || 'Not associated with a curated source']];
  for (const [k,v] of details) { const dt=document.createElement('dt');dt.textContent=k;const dd=document.createElement('dd');dd.textContent=v;dl.append(dt,dd); }
  box.append(dl);
  const fit=fitEstimate(m); const fitTag=document.createElement('div');fitTag.className='tag-list';const tag=document.createElement('span');tag.className=`tag ${fit.kind}`;tag.textContent=fit.label;fitTag.append(tag);box.append(fitTag);const hint=document.createElement('p');hint.className='hint';hint.textContent=fit.detail;box.append(hint);
  if (!m.licenseVerified) { const warning=document.createElement('p');warning.className='hint';warning.textContent='This local model was detected, not license-approved. Only curated catalog entries have verified upstream license metadata. The app cannot verify where a local file came from.';box.append(warning); }
  $('modelTagsEditor').classList.remove('hidden');
}
function launchReadiness(m) {
  if (!m.metadataReadable) return 'Not ready: model metadata unreadable; launch may still work but is unverified';
  if (m.architecture==='qwen3vl' && !($('projectorSelect').value || m.mmprojPath)) return 'Projector required for image input';
  return 'Ready to attempt launch; runtime load and hardware fit are not guaranteed';
}
function runtimeStatus(m) {
  if (!m.metadataReadable) return 'Not confirmed: GGUF metadata could not be read';
  if (m.architecture==='qwen3vl') return 'Qwen3-VL family tested with llamafile 0.10.6 on Windows; exact file/projector still matters';
  if (m.architecture==='qwen3') return 'Qwen3 family tested with llamafile 0.10.6 on Windows; exact model still matters';
  return m.architectureRecognized ? 'Architecture recognized; this exact model has not been runtime-tested here' : 'Not confirmed; architecture is unknown to this controller';
}
function renderCategoryChecks() {
  const root=$('categoryChecks');root.replaceChildren();const m=findModel(state.selectedModel);if(!m)return;
  for(const category of categoryNames){const label=document.createElement('label');label.className='check';const input=document.createElement('input');input.type='checkbox';input.value=category;input.checked=(m.categories||[]).includes(category);const span=document.createElement('span');span.textContent=category[0].toUpperCase()+category.slice(1);label.append(input,span);root.append(label);}
}
function renderProjectorSelect() {
  const select=$('projectorSelect');const current=findModel(state.selectedModel);select.replaceChildren();const none=document.createElement('option');none.value='';none.textContent='No projector selected';select.append(none);
  for(const p of state.models.filter(m=>m.role==='projector')){const o=document.createElement('option');o.value=p.path;o.textContent=`${p.name} — ${p.path}`;select.append(o);}
  if(current?.mmprojPath) select.value=current.mmprojPath;
}
function fillMetadataFields() {
  const m=findModel(state.selectedModel);
  $('modelNotes').value=m?.notes||'';
  $('modelLicense').value=m?.license||'';
  $('modelSourceURL').value=m?.sourceUrl||'';
}
async function saveMetadata() {
  const m=findModel(state.selectedModel);if(!m)return;
  const categories=[...$('categoryChecks').querySelectorAll('input:checked')].map(i=>i.value);
  const body={path:m.path,categories,mmprojPath:$('projectorSelect').value,notes:$('modelNotes').value,license:$('modelLicense').value,sourceUrl:$('modelSourceURL').value,catalogId:$('sourceCatalogSelect').value};
  try { await api('/api/models/metadata',{method:'POST',body:JSON.stringify(body)}); await loadModels(); state.selectedModel=m.path; renderModelSelect(); toast('Model metadata saved.'); }
  catch(err){toast(`Could not save model metadata: ${err.message}`);}
}
async function loadCatalog() {
  try { const result=await api('/api/catalog');state.catalog=result.models||[];renderCatalog();renderCatalogSelect(); }
  catch(err){$('catalogList').textContent=`Catalog unavailable: ${err.message}`;}
}
function renderCatalogSelect() {
  const select=$('sourceCatalogSelect');select.replaceChildren();const blank=document.createElement('option');blank.value='';blank.textContent='No curated source association';select.append(blank);
  for(const item of state.catalog){const option=document.createElement('option');option.value=item.id;option.textContent=`${item.name} · ${item.license}`;select.append(option);}
  const m=findModel(state.selectedModel);select.value = m?.catalogId || '';
}
function renderCatalog() {
  const root=$('catalogList');root.replaceChildren();
  if(!state.catalog.length){root.innerHTML='<p class="empty-state">No curated models are available.</p>';return;}
  for(const item of state.catalog){const card=document.createElement('article');card.className='catalog-card';const title=document.createElement('h3');title.textContent=item.name;card.append(title);const badge=document.createElement('div');badge.className='tag-list';for(const text of [item.license+(item.licenseVerified?' · upstream verified':' · unverified'),(item.distribution==='open-source'?'Open source':item.distribution==='open-weights'?'Open weights':'Distribution unspecified'),...item.categories]){const t=document.createElement('span');t.className='tag good';t.textContent=text;badge.append(t);}card.append(badge);const p=document.createElement('p');p.textContent=item.description;card.append(p);const repo=document.createElement('p');const link=document.createElement('a');try{const source=new URL(item.sourceUrl);if(source.protocol==='https:'){link.href=source.href;link.target='_blank';link.rel='noopener noreferrer';link.textContent=item.repository+' · official source';}else{link.textContent=item.repository+' · invalid source URL';}}catch(_){link.textContent=item.repository+' · invalid source URL';}repo.append(link);card.append(repo);const files=document.createElement('div');files.className='file-list';for(const f of item.files){const row=document.createElement('div');row.className='file-row';row.textContent=`${f.required?'Required':'Optional'} · ${f.role}: ${f.fileName}${f.approxBytes?' · approx. '+bytesLabel(f.approxBytes):''}`;files.append(row);}card.append(files);const list=document.createElement('ol');for(const step of item.manualSteps){const li=document.createElement('li');li.textContent=step;list.append(li);}card.append(list);for(const note of item.notes||[]){const n=document.createElement('p');n.className='hint';n.textContent=note;card.append(n);}const selectBtn=document.createElement('button');selectBtn.className='button secondary';selectBtn.textContent='Associate with selected local model';selectBtn.disabled=!state.selectedModel;selectBtn.addEventListener('click',()=>{ $('sourceCatalogSelect').value=item.id; toast('Catalog source selected. Save model tags and metadata to associate it.'); $('modelTagsEditor').scrollIntoView({behavior:'smooth',block:'center'}); });card.append(selectBtn);root.append(card);}
}

async function detectHardware() {
  const button=$('detectHardwareBtn');button.disabled=true;button.textContent='Detecting…';
  try {state.hardware=await api('/api/hardware');renderHardware();renderModelDetails();toast('Hardware detection complete.');}
  catch(err){toast(`Hardware detection failed: ${err.message}`);}
  finally{button.disabled=false;button.textContent='Detect hardware';}
}
function renderHardware() {
  const h=state.hardware;if(!h)return;$('hardwarePanel').classList.remove('hidden');$('hardwareTimestamp').textContent=new Date().toLocaleTimeString();const root=$('hardwareSummary');root.replaceChildren();
  const metrics=[['CPU',h.cpu,`${h.physicalCores||'?'} cores · ${h.logicalProcessors||'?'} logical`],['System RAM',bytesLabel(h.ramBytes),`${bytesLabel(h.availableRamBytes)} available`],['Operating system',`${h.os}/${h.architecture}`,'Detected at request time']];
  for(const g of h.gpus||[])metrics.push(['GPU',g.name,`${g.vendor} · ${g.vramMB?g.vramMB+' MiB VRAM':'VRAM unknown'}${g.usedVRAMMB?` · ${g.usedVRAMMB} MiB in use`:''} · ${g.vramSource||'source unknown'}`]);
  for(const [label,value,sub] of metrics){const m=document.createElement('div');m.className='metric';const l=document.createElement('div');l.className='label';l.textContent=label;const v=document.createElement('div');v.className='value';v.textContent=value||'Unknown';const s=document.createElement('div');s.className='subvalue';s.textContent=sub||'';m.append(l,v,s);root.append(m);}
  const notes=$('hardwareNotes');notes.replaceChildren();for(const note of h.notes||[]){const li=document.createElement('li');li.textContent=note;notes.append(li);}if(!h.notes?.length){const li=document.createElement('li');li.textContent='Resource estimates are approximate and do not guarantee model startup or performance.';notes.append(li);}
}
function readProfileForm() {
  const p={modelPath:state.selectedModel,mmprojPath:$('projectorSelect').value,autoPort:$('autoPortInput').checked};
  for(const [key,id] of Object.entries(fields)){const el=$(id);p[key]=el.type==='checkbox'?el.checked:(el.type==='number'?Number(el.value):el.value);}
  return p;
}
function fillProfile(p) {
  // Profiles can outlive UI revisions. Skip controls absent from this UI instead of
  // throwing 'Cannot set properties of null (setting value)' and blocking launch.
  for(const [key,id] of Object.entries(fields)){
    const el=$(id);
    if(el && p[key]!==undefined && p[key]!==null){
      if(el.type==='checkbox') el.checked=!!p[key];
      else el.value=String(p[key]);
    }
  }
  const noMmap=$('noMmapInput'); if(noMmap) noMmap.checked=!!p.noMmap;
  const mlock=$('mlockInput'); if(mlock) mlock.checked=!!p.mlock;
  const projector=$('projectorSelect'); if(projector && p.mmprojPath) projector.value=p.mmprojPath;
  renderModelDetails();
}
async function loadProfile(path) {
  if(!path)return;
  if(state.profiles[path]){fillProfile(state.profiles[path]);return;}
  try{const result=await api('/api/settings?modelPath='+encodeURIComponent(path));state.profiles[path]=result.profile;fillProfile(result.profile);}
  catch(err){setStatus(`Could not load model settings: ${err.message}`,'error');}
}
async function saveProfile() {
  if(!state.selectedModel){toast('Select a detected model first.');return null;}
  const p=readProfileForm();delete p.autoPort;
  try{const result=await api('/api/settings',{method:'POST',body:JSON.stringify({profile:p})});state.profiles[p.modelPath]=result.profile;setStatus('Settings saved for this model. Offline mode remains enforced.','success');toast('Model settings saved.');return result.profile;}
  catch(err){setStatus(err.message,'error');toast(`Settings were not saved: ${err.message}`);return null;}
}
async function startServer() {
  if(!state.selectedModel){toast('Select a detected model first.');return;}
  const button=$('startServerBtn');button.disabled=true;button.textContent='Starting…';setStatus('Checking settings and port…');
  try{
    try { state.hardware = await api('/api/hardware'); renderHardware(); renderModelDetails(); } catch (_) {}
    const model=findModel(state.selectedModel);const active=state.servers.filter(s=>['running','starting','unresponsive'].includes(s.status));
    if(model?.architecture==='qwen3vl' && !$('projectorSelect').value && !model.mmprojPath && !confirm('No vision projector is selected. The model may start without image input, but vision requests are likely to fail. Continue?'))throw new Error('Launch cancelled: projector not selected.');
    if($('serverModeSelect').value==='single' && active.length){const ids=active.map(s=>s.id);if(!confirm(`Single-active-model mode is enabled. Stop ${active.length} server(s) owned by this controller before starting the new one? Unrelated processes will not be touched.`))throw new Error('Launch cancelled.');for(const id of ids)await api(`/api/servers/${encodeURIComponent(id)}`,{method:'DELETE'});await refreshServers();}
    if($('serverModeSelect').value==='concurrent' && active.length){const fit=fitEstimate(model);if(!confirm(`Concurrent servers use additional RAM/VRAM. ${active.length} server(s) are already active. New model estimate: ${fit.label}. Continue?`))throw new Error('Launch cancelled.');}
    if(Number($('parallelInput').value)>1 && !confirm('Parallel requests increase memory demand. Continue with this setting?'))throw new Error('Launch cancelled after parallel-request warning.');
    const fit=fitEstimate(model);if(fit.kind==='bad' && !confirm(`Resource warning: ${fit.detail}

Attempt launch anyway?`))throw new Error('Launch cancelled after resource warning.');
    const profile=readProfileForm();const autoPort=profile.autoPort;delete profile.autoPort;
    const settings=await api('/api/settings',{method:'POST',body:JSON.stringify({profile})});state.profiles[profile.modelPath]=settings.profile;
    let result;
    try{result=await api('/api/servers',{method:'POST',body:JSON.stringify({...settings.profile,autoPort})});}
    catch(err){if(!autoPort && /port \d+ is occupied/i.test(err.message) && confirm(`${err.message}\n\nChoose an available alternative port automatically? The process using the occupied port will not be stopped.`)){result=await api('/api/servers',{method:'POST',body:JSON.stringify({...settings.profile,autoPort:true})});}else{throw err;}}
    setStatus(`Server ${result.status} on http://127.0.0.1:${result.port}. Startup may take a little while.`,'success');await refreshServers();toast(`Server launched on port ${result.port}.`);
  }catch(err){console.error('PortableAI startServer failed:',err);setStatus(err.message,'error');toast(`Server did not start: ${err.message}`);}
  finally{button.disabled=false;button.textContent='Start model server';}
}
async function refreshServers() {
  try{
    const result=await api('/api/servers');const now=Date.now();
    for(const s of result.servers||[]){const old=state.servers.find(x=>x.id===s.id);if(old&&!['stopped','exited'].includes(old.status)&&['stopped','exited'].includes(s.status))state.stoppedGrace.set(s.id,now+5000);}
    state.servers=result.servers||[];renderServers(state.servers,'serversList','No servers are owned by this controller.');
    const running=state.servers.filter(s=>!['stopped','exited'].includes(s.status)||(state.stoppedGrace.get(s.id)||0)>now);
    renderServers(running,'runningServersList','No running servers.');
    for(const [id,deadline] of state.stoppedGrace)if(deadline<=now)state.stoppedGrace.delete(id);
  }
  catch(err){$('serversList').textContent=`Could not retrieve server list: ${err.message}`;$('runningServersList').textContent=`Could not retrieve server list: ${err.message}`;}
}
function renderServers(servers,rootId,emptyMessage) {
  const root=$(rootId);if(!root)return;root.replaceChildren();if(!servers.length){const p=document.createElement('p');p.className='empty-state';p.textContent=emptyMessage;root.append(p);return;}
  for(const s of servers){
    const card=document.createElement('div');card.className='server-card';
    const main=document.createElement('div');main.className='server-main';
    const name=document.createElement('div');name.className='server-name';name.textContent=s.modelName;
    const meta=document.createElement('div');meta.className='server-meta';meta.textContent=`${s.status} · PID ${s.pid||'—'} · port ${s.port} · ${s.gpuBackend} · ctx ${s.contextSize}${s.exitError?' · '+s.exitError:''}`;
    main.append(name,meta);
    const keyGroup=document.createElement('div');keyGroup.className='api-key-group';
    const keyLabel=document.createElement('label');keyLabel.textContent='Server API key';
    const keyRow=document.createElement('div');keyRow.className='api-key-row';
    const keyInput=document.createElement('input');keyInput.className='control api-key-input';keyInput.id=`api-key-${s.id}`;keyInput.type='password';keyInput.readOnly=true;keyInput.autocomplete='off';keyInput.spellcheck=false;
    keyInput.value=s.apiKey||'';keyInput.placeholder=s.apiKey?'':'This runtime does not support API keys';keyInput.setAttribute('aria-label',`API key for ${s.modelName}`);keyLabel.htmlFor=keyInput.id;
    const makeIcon=(kind)=>{const svg=document.createElementNS('http://www.w3.org/2000/svg','svg');svg.setAttribute('viewBox','0 0 24 24');svg.setAttribute('width','18');svg.setAttribute('height','18');svg.setAttribute('fill','none');svg.setAttribute('stroke','currentColor');svg.setAttribute('stroke-width','1.8');svg.setAttribute('stroke-linecap','round');svg.setAttribute('stroke-linejoin','round');svg.setAttribute('aria-hidden','true');if(kind==='copy'){const a=document.createElementNS(svg.namespaceURI,'rect');a.setAttribute('x','8');a.setAttribute('y','8');a.setAttribute('width','12');a.setAttribute('height','12');a.setAttribute('rx','2');const b=document.createElementNS(svg.namespaceURI,'path');b.setAttribute('d','M16 8V5a2 2 0 0 0-2-2H5a2 2 0 0 0-2 2v9a2 2 0 0 0 2 2h3');svg.append(a,b);}else{const path=document.createElementNS(svg.namespaceURI,'path');path.setAttribute('d',kind==='hidden'?'M3 3l18 18M10.6 10.6a2 2 0 0 0 2.8 2.8M9.9 5.2A10.7 10.7 0 0 1 12 5c6.4 0 10 7 10 7a16.6 16.6 0 0 1-4.1 4.8M6.2 6.2C3.5 8 2 12 2 12s3.6 7 10 7a10.4 10.4 0 0 0 4-.8':'M2 12s3.6-7 10-7 10 7 10 7-3.6 7-10 7S2 12 2 12Z');svg.append(path);if(kind==='visible'){const pupil=document.createElementNS(svg.namespaceURI,'circle');pupil.setAttribute('cx','12');pupil.setAttribute('cy','12');pupil.setAttribute('r','2.5');svg.append(pupil);}}return svg;};
    const toggle=document.createElement('button');toggle.className='button secondary small api-key-toggle icon-button';toggle.type='button';toggle.title='Show API key';toggle.setAttribute('aria-label',`Show API key for ${s.modelName}`);toggle.disabled=!s.apiKey;toggle.append(makeIcon('hidden'));
    toggle.addEventListener('click',()=>{const reveal=keyInput.type==='password';keyInput.type=reveal?'text':'password';toggle.replaceChildren(makeIcon(reveal?'visible':'hidden'));toggle.title=reveal?'Hide API key':'Show API key';toggle.setAttribute('aria-label',`${reveal?'Hide':'Show'} API key for ${s.modelName}`);});
    const copy=document.createElement('button');copy.className='button secondary small api-key-copy icon-button';copy.type='button';copy.title='Copy API key';copy.setAttribute('aria-label',`Copy API key for ${s.modelName}`);copy.disabled=!s.apiKey;copy.append(makeIcon('copy'));
    copy.addEventListener('click',async()=>{if(!s.apiKey)return;try{if(navigator.clipboard&&navigator.clipboard.writeText){await navigator.clipboard.writeText(s.apiKey);}else{keyInput.type='text';keyInput.focus();keyInput.select();const copied=document.execCommand('copy');keyInput.type='password';keyInput.setSelectionRange(0,0);if(!copied)throw new Error('Clipboard access is unavailable');}toast('API key copied. Paste it into llamafile’s Enter API Key prompt.');}catch(err){keyInput.type='text';keyInput.focus();keyInput.select();toast('Automatic copy was blocked. The key is selected; press Ctrl+C to copy it.');}});
    keyRow.append(keyInput,toggle,copy);keyGroup.append(keyLabel,keyRow);main.append(keyGroup);
    const actions=document.createElement('div');actions.className='server-actions';
    const health=document.createElement('button');health.className='button secondary small';health.textContent='Check health';health.addEventListener('click',()=>checkServerHealth(s));
    const logs=document.createElement('button');logs.className='button secondary small';logs.textContent='View log';logs.addEventListener('click',()=>viewLogs(s));
    const stop=document.createElement('button');stop.className='button secondary small';stop.textContent='Stop owned server';stop.disabled=['stopped','exited'].includes(s.status);stop.addEventListener('click',()=>stopServer(s));
    actions.append(health,logs,stop);card.append(main,actions);root.append(card);
  }
}
async function checkServerHealth(s){try{const result=await api(`/api/servers/${encodeURIComponent(s.id)}/health`);toast(`Server on port ${s.port} is healthy: ${JSON.stringify(result).slice(0,180)}`);}catch(err){toast(`Health check failed: ${err.message}`);}}
async function viewLogs(s){try{const text=await fetch(`/api/servers/${encodeURIComponent(s.id)}/logs`).then(async r=>{if(!r.ok)throw new Error(await r.text());return r.text();});$('logContent').textContent=text||'(Log is empty.)';$('logDialog').showModal();}catch(err){toast(`Could not read log: ${err.message}`);}}
async function stopServer(s){if(!confirm(`Stop only the server process owned by PortableAI on port ${s.port}?`))return;try{await api(`/api/servers/${encodeURIComponent(s.id)}`,{method:'DELETE'});toast(`Owned server on port ${s.port} stopped.`);await refreshServers();}catch(err){toast(`Stop failed: ${err.message}`);}}


function showView(view){state.currentView=view;document.querySelectorAll('main [data-view]').forEach(el=>{el.classList.toggle('view-hidden',el.dataset.view!==view);});document.querySelectorAll('.nav-item').forEach(b=>b.classList.toggle('active',b.dataset.view===view));if(view==='catalog')loadCatalog();if(view==='running'||view==='owned')refreshServers();}
async function loadSetupGuide(){const target=$('setupMarkdown');if(!target)return;try{const response=await fetch('/Docs/INITIAL-SETUP.md');if(!response.ok)throw new Error(`HTTP ${response.status}`);const source=await response.text();renderMarkdownSafe(source,target);}catch(err){target.textContent=`Could not load Docs/INITIAL-SETUP.md: ${err.message}`;}}

init();
setInterval(()=>{if(state.currentView==='running'||state.currentView==='owned')refreshServers();},1000);
