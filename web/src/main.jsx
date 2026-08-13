// ai-form-backend console - AGPL-3.0
import React from 'react';
import ReactDOM from 'react-dom/client';
import App from './App.jsx';
// Semi UI 2.x 组件自带样式引入,无需全量 css
import './styles.css';
import './bridge.js'; // 浏览器插件登录态桥接(网站登录 → 插件自动登录)

ReactDOM.createRoot(document.getElementById('root')).render(<App />);
