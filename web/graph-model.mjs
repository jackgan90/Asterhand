export const graphLimits = {nodes:64, edges:512, parallelism:16};
const newID = () => globalThis.crypto.randomUUID();
export function newGraph(id = newID) {
  const start = {id:id(),kind:'start',name:'',x:40,y:100};
  const script = {id:id(),kind:'script',name:'',script:'',arguments:[],directory:'',failure_policy:'abort',x:290,y:100};
  const end = {id:id(),kind:'end',name:'',x:540,y:100};
  return {nodes:[start,script,end],edges:[{from:start.id,to:script.id},{from:script.id,to:end.id}],parallelism:4};
}
export const copyGraph = graph => structuredClone(graph);
// Optional fields omitted by the server and explicit empty UI values represent
// the same editable content. Keep invalid numeric drafts intact for comparison.
export function graphContent(graph) {
  if (!graph) return graph;
  return {...graph, nodes: graph.nodes.map(n => n.kind !== 'script' ? n : {
    ...n, script: n.script ?? '', directory: n.directory ?? '', arguments: n.arguments ?? [],
    timeout_seconds: n.timeout_seconds ?? null, failure_policy: n.failure_policy || 'abort',
  })};
}
const issue = (key, node = '') => ({messageKey:key,messageArgs:{node}});

export function edgeIssue(graph, from, to) {
  if (!graph.nodes.some(n=>n.id===from) || !graph.nodes.some(n=>n.id===to) || from===to ||
      graph.nodes.find(n=>n.id===from)?.kind==='end' || graph.nodes.find(n=>n.id===to)?.kind==='start' ||
      graph.edges.some(e=>e.from===from&&e.to===to) || graph.edges.length>=graphLimits.edges) return issue('err.graph.edge');
  if (walk(to,graph.edges).has(from)) return issue('err.graph.cycle');
  return null;
}
function walk(start, edges, reverse = false) {
  const seen = new Set([start]), queue = [start];
  while(queue.length){
    const current = queue.shift();
    for(const edge of edges){
      const source = reverse ? edge.to : edge.from, target = reverse ? edge.from : edge.to;
      if(source===current&&!seen.has(target)){seen.add(target);queue.push(target);}
    }
  }
  return seen;
}
export function graphIssue(graph) {
  if(!graph||graph.nodes.length<3||graph.nodes.length>graphLimits.nodes||graph.edges.length>graphLimits.edges) return issue('err.graph.size');
  if(!Number.isInteger(graph.parallelism)||graph.parallelism<1||graph.parallelism>graphLimits.parallelism) return issue('err.graph.parallelism');
  const starts=graph.nodes.filter(n=>n.kind==='start'),ends=graph.nodes.filter(n=>n.kind==='end');
  if(starts.length!==1||ends.length!==1) return issue('err.graph.endpoints');
  const seen=new Set(),accepted={nodes:graph.nodes,edges:[]};
  for(const n of graph.nodes){
    if(!/^[a-zA-Z0-9_-]{16,64}$/.test(n.id)||seen.has(n.id)) return issue('err.graph.identity',n.name);
    seen.add(n.id);
    if(!['start','end','script'].includes(n.kind)) return issue('err.graph.node',n.name);
    if(n.kind==='script'&&(!n.name.trim()||!n.script.trim())) return issue('err.graph.node',n.name);
  }
  for(const e of graph.edges){const error=edgeIssue(accepted,e.from,e.to);if(error)return error;accepted.edges.push(e);}
  const reached=walk(starts[0].id,graph.edges),finishes=walk(ends[0].id,graph.edges,true);
  for(const n of graph.nodes) if(!reached.has(n.id)||!finishes.has(n.id)) return issue('err.graph.disconnected',n.name);
  return null;
}
export function removeGraphNode(graph,id){
  if(graph.nodes.find(n=>n.id===id)?.kind!=='script')return false;
  graph.nodes=graph.nodes.filter(n=>n.id!==id);graph.edges=graph.edges.filter(e=>e.from!==id&&e.to!==id);
  return true;
}
