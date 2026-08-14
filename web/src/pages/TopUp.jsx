// ai-form-backend console - AGPL-3.0
// 购买套餐:套餐卡片 → 选支付方式 → 易支付收银台(表单跳转);回跳带 ?pay= 提示结果。
import React, { useContext, useEffect, useState } from 'react';
import { useSearchParams } from 'react-router-dom';
import { Card, Button, Toast, Typography, Radio, RadioGroup, Row, Col, Tag } from '@douyinfe/semi-ui';
import { get, post } from '../api.js';
import { MeContext } from '../App.jsx';

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

export default function TopUp() {
  const { reload } = useContext(MeContext);
  const [plans, setPlans] = useState([]);
  const [method, setMethod] = useState('alipay');
  const [buying, setBuying] = useState(0);
  const [sp, setSp] = useSearchParams();

  useEffect(() => {
    get('/v1/subscription/plans').then((d) => setPlans(d.plans)).catch((e) => Toast.error(e.message));
  }, []);

  useEffect(() => {
    const pay = sp.get('pay');
    if (!pay) return;
    if (pay === 'success') { Toast.success('支付成功,套餐已到账'); reload(); }
    else if (pay === 'fail') Toast.error('支付未完成');
    else Toast.info('支付处理中,稍后在个人中心查看');
    sp.delete('pay');
    setSp(sp, { replace: true });
  }, []); // eslint-disable-line react-hooks/exhaustive-deps

  const buy = async (plan) => {
    setBuying(plan.id);
    try {
      const d = await post('/v1/subscription/pay', { planId: plan.id, method });
      submitPayForm(d.url, d.params);
    } catch (e) {
      Toast.error(e.message);
      setBuying(0);
    }
  };

  return (
    <div className="topup-page">
      <Card className="payment-method-card">
        <span className="field-label">支付方式</span>
        <RadioGroup value={method} onChange={(e) => setMethod(e.target.value)} type="button">
          <Radio value="alipay">支付宝</Radio>
          <Radio value="wxpay">微信支付</Radio>
        </RadioGroup>
      </Card>
      <Row gutter={16}>
        {plans.map((p) => (
          <Col xs={24} sm={12} lg={8} key={p.id}>
            <Card
              title={p.name}
              headerExtraContent={
                p.planType === 'pack'
                  ? <Tag color="green">积分包 · {p.durationDays} 天有效</Tag>
                  : <Tag color="blue">订阅 · {p.durationDays} 天</Tag>
              }
              className="plan-card"
            >
              <Typography.Title heading={2} className="plan-price"><small>¥</small>{(p.priceCents / 100).toFixed(2)}</Typography.Title>
              <Typography.Paragraph type="secondary">
                {p.planType === 'pack' ? `${p.credits} 积分` : `插件全部功能 · 固定流程录入不限量 · 每月含 ${p.credits} 积分`}
              </Typography.Paragraph>
              <Typography.Paragraph type="tertiary" size="small">
                {p.planType === 'pack'
                  ? '需持有有效的个人版订阅;购买的积分 365 天内有效,订阅断档时冻结、复订即恢复'
                  : '首次开通加赠 1500 积分(60 天有效);月含积分到期清零,可随时叠加加油包'}
              </Typography.Paragraph>
              <Button theme="solid" block loading={buying === p.id} onClick={() => buy(p)}>立即购买</Button>
            </Card>
          </Col>
        ))}
      </Row>
      {plans.length === 0 && <Typography.Text type="tertiary">暂无在售套餐</Typography.Text>}
    </div>
  );
}
