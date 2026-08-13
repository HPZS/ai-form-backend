// ai-form-backend console - AGPL-3.0
// 控制台布局:左侧导航(管理员多出管理菜单) + 顶栏 + 内容区。
import React, { useContext } from 'react';
import { Outlet, useNavigate, useLocation } from 'react-router-dom';
import { Nav, Layout, Avatar, Dropdown, Typography } from '@douyinfe/semi-ui';
import {
  IconHome, IconCreditCard, IconHistogram, IconPriceTag, IconSetting,
  IconServer, IconUserGroup, IconExit,
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

  return (
    <Layout style={{ height: '100vh' }}>
      <Sider style={{ backgroundColor: 'var(--semi-color-bg-1)' }}>
        <Nav
          style={{ height: '100%' }}
          items={items}
          selectedKeys={[location.pathname]}
          defaultOpenKeys={['admin']}
          onSelect={({ itemKey }) => { if (String(itemKey).startsWith('/')) navigate(String(itemKey)); }}
          header={{ text: 'AI智能录入助手' }}
          footer={{ collapseButton: true }}
        />
      </Sider>
      <Layout>
        <Header style={{
          backgroundColor: 'var(--semi-color-bg-1)', display: 'flex', alignItems: 'center',
          justifyContent: 'flex-end', padding: '0 24px', height: 56,
          borderBottom: '1px solid var(--semi-color-border)',
        }}>
          <Dropdown
            position="bottomRight"
            render={
              <Dropdown.Menu>
                <Dropdown.Item icon={<IconExit />} onClick={logout}>退出登录</Dropdown.Item>
              </Dropdown.Menu>
            }
          >
            <span style={{ cursor: 'pointer', display: 'flex', alignItems: 'center', gap: 8 }}>
              <Avatar size="small" color="blue">{me.email[0].toUpperCase()}</Avatar>
              <Typography.Text>{me.email}{me.role === 'admin' ? '(管理员)' : ''}</Typography.Text>
            </span>
          </Dropdown>
        </Header>
        <Content style={{ padding: 24, overflow: 'auto', backgroundColor: 'var(--semi-color-bg-0)' }}>
          <Outlet />
          <div style={{ textAlign: 'center', color: 'var(--semi-color-text-2)', fontSize: 12, marginTop: 40, paddingBottom: 16 }}>
            <a href="https://github.com/HPZS/ai-form-backend" target="_blank" rel="noreferrer" style={{ color: 'inherit' }}>源码(AGPL-3.0)</a>
            {' · '}Frontend design and development by New API contributors.{' · '}
            <a href="https://github.com/QuantumNous/new-api" target="_blank" rel="noreferrer" style={{ color: 'inherit' }}>new-api</a>
          </div>
        </Content>
      </Layout>
    </Layout>
  );
}
