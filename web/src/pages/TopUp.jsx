// ai-form-backend console - AGPL-3.0
// 购买套餐:选支付方式 → 套餐卡片点"立即购买" → 易支付收银台(表单跳转);回跳带 ?pay= 提示结果。
import React, { useContext, useEffect, useState } from 'react';
import { useSearchParams } from 'react-router-dom';
import { Check } from 'lucide-react';
import { get, post } from '../api.js';
import { MeContext } from '../App.jsx';
import { Button, Segmented, Tag, Spinner, toast, fmtNum } from '../ui';

// 用隐藏表单 POST 跳转到易支付收银台
function submitPayForm(url, params) {
  const form = document.createElement('form');
  form.method = 'POST';
  form.action = url;
  for (const [k, v] of Object.entries(params || {})) {
    const input = document.createElement('input');
    input.type = 'hidden';
    input.name = k;
    input.value = v;
    form.appendChild(input);
  }
  document.body.appendChild(form);
  form.submit();
}

function planPoints(p) {
  return p.planType === 'pack'
    ? [`${fmtNum(p.credits)} 积分,${p.durationDays} 天内有效`, '需持有有效的个人版订阅', '订阅断档时冻结,复订即恢复']
    : ['插件全部功能', '固定流程录入不限量', `每月含 ${fmtNum(p.credits)} 积分,到期清零`, '首次开通加赠 1500 积分(60 天有效)', '可随时叠加加油包'];
}

function priceUnit(p) {
  if (p.planType === 'pack') return '';
  return p.durationDays === 30 ? '/ 月' : `/ ${p.durationDays} 天`;
}

export default function TopUp() {
  const { reload } = useContext(MeContext);
  const [plans, setPlans] = useState(null);
  const [method, setMethod] = useState('alipay');
  const [buying, setBuying] = useState(0);
  const [sp, setSp] = useSearchParams();

  useEffect(() => {
    get('/v1/subscription/plans').then((d) => setPlans(d.plans)).catch((e) => { toast.error(e.message); setPlans([]); });
  }, []);

  useEffect(() => {
    const pay = sp.get('pay');
    if (!pay) return;
    if (pay === 'success') { toast.success('支付成功,套餐已到账'); reload(); }
    else if (pay === 'fail') toast.error('支付未完成');
    else toast.info('支付处理中,稍后在个人中心查看');
    sp.delete('pay');
    setSp(sp, { replace: true });
  }, []); // eslint-disable-line react-hooks/exhaustive-deps

  const buy = async (plan) => {
    setBuying(plan.id);
    try {
      const d = await post('/v1/subscription/pay', { planId: plan.id, method });
      submitPayForm(d.url, d.params);
    } catch (e) {
      toast.error(e.message);
      setBuying(0);
    }
  };

  if (plans === null) return <div className="table-loading"><Spinner size="lg" /></div>;
  const featuredId = plans.find((p) => p.planType === 'base')?.id;

  return (
    <div className="topup">
      <div className="pay-bar">
        <span className="muted">选择套餐后跳转收银台完成支付,支付成功自动到账</span>
        <div className="pay-method">
          <span className="field-label">支付方式</span>
          <Segmented value={method} onChange={setMethod}
            options={[{ value: 'alipay', label: '支付宝' }, { value: 'wxpay', label: '微信支付' }]} />
        </div>
      </div>

      {plans.length === 0 ? (
        <div className="card"><div className="empty"><span>暂无在售套餐</span></div></div>
      ) : (
        <div className="plans">
          {plans.map((p) => {
            const featured = p.id === featuredId;
            return (
              <article key={p.id} className={'plan' + (featured ? ' featured' : '')}>
                <div className="plan-top">
                  <span className="plan-name">{p.name}</span>
                  {featured ? <Tag tone="accent">推荐</Tag> : <Tag>{p.planType === 'pack' ? '加油包' : '订阅'}</Tag>}
                </div>
                <div className="plan-price num">
                  <small>¥</small>{(p.priceCents / 100).toFixed(2)}
                  {priceUnit(p) && <span className="plan-unit">{priceUnit(p)}</span>}
                </div>
                <ul className="plan-points">
                  {planPoints(p).map((t) => <li key={t}><Check />{t}</li>)}
                </ul>
                <Button variant={featured ? 'primary' : 'default'} size="lg" block loading={buying === p.id} onClick={() => buy(p)}>
                  立即购买
                </Button>
              </article>
            );
          })}
        </div>
      )}
    </div>
  );
}
