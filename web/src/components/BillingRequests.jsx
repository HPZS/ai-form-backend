import React, { useEffect, useState } from 'react';
import { get, post } from '../api';
import { Card, Table, Pagination, Button, Input, NumberInput, Dialog, toast, fmtTime } from '../ui';
const reasons = { subscription_included:'订阅包含', successful_call:'成功调用', no_usable_result:'无可用匹配，未扣费', execution_failed:'执行失败，未扣费', legacy_execution_expired:'历史请求未完成' };
const statuses = { ok:'已结算', pending:'执行中', settlement_pending:'待结算', failed:'失败未扣费', ai_invalid:'结果无效', upstream_error:'上游失败' };
export default function BillingRequests({ userId }) {
 const [page,setPage]=useState(1), [data,setData]=useState({entries:[],total:0}), [loading,setLoading]=useState(false), [revision,setRevision]=useState(0);
 const [refund,setRefund]=useState(null), [busy,setBusy]=useState(false);
 useEffect(()=>{let current=true;setLoading(true);get(`/v1/billing/requests?page=${page}${userId ? `&userId=${userId}`:''}`).then(d=>{if(current)setData(d)}).catch(e=>toast.error(e.message)).finally(()=>{if(current)setLoading(false)});return()=>{current=false}},[page,userId,revision]);
 const submitRefund=async()=>{
  if(!Number.isSafeInteger(refund.amount)||refund.amount<1||refund.amount>refund.max||!refund.reason.trim()){toast.warning('请输入有效返还金额和原因');return}
  setBusy(true);
  // 失败时保留同一返还 ID；网络重试不会多次返还。
  try{await post('/v1/admin/billing/refunds',{userId,requestId:refund.requestId,refundId:refund.id,amount:refund.amount,reason:refund.reason});toast.success('已返还并记入关联流水');setRefund(null);setRevision(x=>x+1)}catch(e){toast.error(e.message)}finally{setBusy(false)}
 };
 const columns=[
  {title:'时间 / 请求',key:'createdAt',render:r=><div><span>{fmtTime(r.createdAt)}</span><div className="cell-sub mono">{r.requestId}</div><div className="cell-sub">任务：{r.taskId||'历史未归属'}</div></div>},
  {title:'能力 / 原因',key:'capability',render:r=><div>{r.name||r.capability}<div className="cell-sub">{reasons[r.reason]||r.reason||'历史计费规则'} · {r.mode==='included'?'订阅包含':r.mode==='per_call'?'逐次积分':'历史政策'}</div></div>},
  {title:'状态',key:'status',render:r=>statuses[r.status]||r.status},
  {title:'实扣 / 返还 / 净扣',key:'charged',render:r=>`${r.charged} / ${r.refunded} / ${r.charged-r.refunded}`},
  ...(userId?[{title:'操作',key:'refund',render:r=>r.charged>r.refunded?<Button size="sm" onClick={()=>setRefund({id:crypto.randomUUID(),requestId:r.requestId,max:r.charged-r.refunded,amount:r.charged-r.refunded,reason:''})}>返还积分</Button>:null}]:[])
 ];
 return <Card flush title="AI 调用账单" extra={<Button size="sm" onClick={()=>setRevision(x=>x+1)}>刷新</Button>}>
  <Table columns={columns} rows={data.entries} rowKey="requestId" loading={loading} empty="暂无调用记录"/>
  <Pagination page={page} pageSize={20} total={data.total} loading={loading} onChange={setPage}/>
  {refund&&<Dialog open title="返还原请求积分" onClose={()=>{if(!busy)setRefund(null)}}><div className="stack-16"><p>原请求 {refund.requestId}。最多可返还 {refund.max} 积分；原积分过期时发放 30 天补偿积分。</p><NumberInput prefix="返还积分" min={1} max={refund.max} value={refund.amount} onChange={amount=>setRefund({...refund,amount})}/><Input placeholder="返还原因（必填）" maxLength={100} value={refund.reason} onChange={reason=>setRefund({...refund,reason})}/><Button variant="primary" loading={busy} onClick={submitRefund}>确认返还并记账</Button></div></Dialog>}
 </Card>
}
