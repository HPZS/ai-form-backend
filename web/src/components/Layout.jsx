// ai-form-backend console - AGPL-3.0
// 控制台布局:左侧导航(管理员多出管理菜单) + 顶栏 + 内容区。
import React, { useContext } from 'react';
import { Outlet, useNavigate, useLocation } from 'react-router-dom';
import { Nav, Layout, Avatar, Dropdown, Typography, Button, Tag } from '@douyinfe/semi-ui';
import {
  IconHome, IconCreditCard, IconHistogram, IconPriceTag, IconSetting,
  IconServer, IconUserGroup, IconExit, IconChevronDown,
} from '@douyinfe/semi-icons';
import { MeContext } from '../App.jsx';
import { logout } from '../api.js';

const { Header, Sider, Content } = Layout;

export default function ConsoleLayout() {
  const { me } = useContext(MeContext);
  const navigate = useNavigate();
  const location = useLocation();

  const items = [
    { itemKey: '/', text: '个人中心', icon: <IconHome /> },
    { itemKey: '/topup', text: '购买套餐', icon: <IconCreditCard /> },
    { itemKey: '/ledger', text: '积分流水', icon: <IconHistogram /> },
  ];
  if (me.role === 'admin') {
    items.push({
      itemKey: 'admin', text: '系统管理', icon: <IconSetting />,
      items: [
        { itemKey: '/admin/plans', text: '套餐管理', icon: <IconPriceTag /> },
        { itemKey: '/admin/capabilities', text: '能力配置', icon: <IconSetting /> },
        { itemKey: '/admin/upstreams', text: 'AI 上游', icon: <IconServer /> },
        { itemKey: '/admin/users', text: '用户管理', icon: <IconUserGroup /> },
      ],
    });
  }

  const pageMeta = {
    '/': ['个人中心', '查看账户额度、有效订阅与安全设置'],
    '/topup': ['购买套餐', '选择适合你的积分套餐'],
    '/ledger': ['积分流水', '查看积分充值与消费明细'],
    '/admin/plans': ['套餐管理', '管理套餐价格、额度与销售状态'],
    '/admin/capabilities': ['能力配置', '配置各项 AI 能力的计费与模型参数'],
    '/admin/upstreams': ['AI 上游', '管理 OpenAI 兼容端点与故障切换顺序'],
    '/admin/users': ['用户管理', '查看用户并管理账号状态'],
  }[location.pathname] || ['控制台', 'AI 智能录入助手'];

  return (
    <Layout className="console-shell">
      <Header className="app-header">
        <div className="brand-lockup" onClick={() => navigate('/')} role="link" tabIndex={0} onKeyDown={(e) => e.key === 'Enter' && navigate('/')}>
          <span className="brand-mark" aria-hidden="true"><span /></span>
          <span className="brand-name">AI 智能录入助手</span>
        </div>
        <div className="header-actions">
          {me.role === 'admin' && <Tag color="blue" className="admin-tag">管理员</Tag>}
          <Dropdown
            position="bottomRight"
            render={
              <Dropdown.Menu>
                <Dropdown.Item icon={<IconExit />} onClick={logout}>退出登录</Dropdown.Item>
              </Dropdown.Menu>
            }
          >
            <Button theme="borderless" className="profile-button">
              <Avatar size="extra-small" color="blue">{me.email[0].toUpperCase()}</Avatar>
              <Typography.Text className="profile-email">{me.email}</Typography.Text>
              <IconChevronDown size="small" />
            </Button>
          </Dropdown>
        </div>
      </Header>
      <Layout className="console-body">
      <Sider className="app-sider">
        <Nav
          className="side-nav"
          items={items}
          selectedKeys={[location.pathname]}
          defaultOpenKeys={['admin']}
          onSelect={({ itemKey }) => { if (String(itemKey).startsWith('/')) navigate(String(itemKey)); }}
        />
      </Sider>
      <Layout className="main-layout">
        <Content className="main-content">
          <div className="page-heading">
            <div>
              <Typography.Title heading={4}>{pageMeta[0]}</Typography.Title>
              <Typography.Text type="tertiary">{pageMeta[1]}</Typography.Text>
            </div>
          </div>
          <div className="page-content"><Outlet /></div>
          <div className="app-footer">
            <a href="https://github.com/HPZS/ai-form-backend" target="_blank" rel="noreferrer" style={{ color: 'inherit' }}>源码(AGPL-3.0)</a>
            {' · '}Frontend design and development by New API contributors.{' · '}
            <a href="https://github.com/QuantumNous/new-api" target="_blank" rel="noreferrer" style={{ color: 'inherit' }}>new-api</a>
          </div>
        </Content>
      </Layout>
      </Layout>
    </Layout>
  );
}
