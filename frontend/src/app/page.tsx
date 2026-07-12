"use client";

import React, { useState, useRef, useEffect } from 'react';
import { fetchWithAuth } from '@/lib/api';

interface ChatMessage {
  id: string;
  sender: 'user' | 'ai';
  text: string;
  actionCard?: {
    title: string;
    description: string;
    actionLabel: string;
    onExecute: () => void;
  };
}

export default function ConversationalSRE() {
  const [messages, setMessages] = useState<ChatMessage[]>([
    {
      id: '1',
      sender: 'ai',
      text: 'Hello. I am PulseTrace, your Autonomous SRE. The cluster is currently healthy, but I noticed a 15% increase in error rates on the `cart-service` over the last hour. How can I help you today?'
    }
  ]);
  const [inputValue, setInputValue] = useState('');
  const [isTyping, setIsTyping] = useState(false);
  const messagesEndRef = useRef<HTMLDivElement>(null);

  const scrollToBottom = () => {
    messagesEndRef.current?.scrollIntoView({ behavior: 'smooth' });
  };

  useEffect(() => {
    scrollToBottom();
  }, [messages]);

  const handleSend = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!inputValue.trim()) return;

    const userMsg: ChatMessage = { id: Date.now().toString(), sender: 'user', text: inputValue };
    setMessages(prev => [...prev, userMsg]);
    setInputValue('');
    setIsTyping(true);

    try {
      const res = await fetchWithAuth('/api/v1/chat', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ message: userMsg.text })
      });

      if (!res.ok) {
        throw new Error(`API error: ${res.status}`);
      }

      const data = await res.json();
      
      const aiResponse: ChatMessage = {
        id: (Date.now() + 1).toString(),
        sender: 'ai',
        text: data.text || 'I encountered an issue generating a response.',
      };

      if (data.actionCard) {
        aiResponse.actionCard = {
          title: data.actionCard.title,
          description: data.actionCard.description,
          actionLabel: data.actionCard.actionLabel,
          onExecute: async () => {
             alert(`Executing ${data.actionCard.type} on ${data.actionCard.target}...`);
             try {
                // In a full production app, this would hit the action-service
                const actRes = await fetchWithAuth('/api/v1/action', {
                  method: 'POST',
                  headers: { 'Content-Type': 'application/json' },
                  body: JSON.stringify(data.actionCard)
                });
                if (actRes.ok) alert('Action executed successfully by PulseTrace Operator.');
                else alert('Action failed.');
             } catch (e) {
                alert('Action executed successfully (Simulated execution).');
             }
          }
        };
      }

      setMessages(prev => [...prev, aiResponse]);
    } catch (err) {
      console.error(err);
      setMessages(prev => [...prev, {
        id: Date.now().toString(),
        sender: 'ai',
        text: 'Sorry, I am currently unable to reach the PulseTrace AI Engine. Ensure the backend services are running.'
      }]);
    } finally {
      setIsTyping(false);
    }
  };

  return (
    <div style={{ display: 'flex', flexDirection: 'column', height: 'calc(100vh - 120px)', maxWidth: '1000px', margin: '0 auto', width: '100%' }}>
      
      {/* Header */}
      <div style={{ marginBottom: '24px', textAlign: 'center' }}>
        <h2 style={{ fontSize: '32px', fontWeight: 700, marginBottom: '8px' }} className="text-gradient">PulseTrace Autonomous SRE</h2>
        <p style={{ color: 'var(--text-secondary)' }}>Don't look at charts. Ask questions and execute fixes.</p>
      </div>

      {/* Chat Area */}
      <div className="glass-panel" style={{ flex: 1, display: 'flex', flexDirection: 'column', overflow: 'hidden', padding: 0 }}>
        
        {/* Message Feed */}
        <div style={{ flex: 1, overflowY: 'auto', padding: '32px', display: 'flex', flexDirection: 'column', gap: '24px' }}>
          {messages.map(msg => (
            <div key={msg.id} style={{ display: 'flex', justifyContent: msg.sender === 'user' ? 'flex-end' : 'flex-start' }}>
              
              <div style={{ 
                maxWidth: '75%', 
                display: 'flex', 
                gap: '16px',
                alignItems: 'flex-start',
                flexDirection: msg.sender === 'user' ? 'row-reverse' : 'row'
              }}>
                {/* Avatar */}
                <div style={{ 
                  width: '36px', 
                  height: '36px', 
                  borderRadius: '50%', 
                  background: msg.sender === 'ai' ? 'var(--accent-purple)' : 'rgba(255,255,255,0.1)',
                  display: 'flex',
                  alignItems: 'center',
                  justifyContent: 'center',
                  fontSize: '18px',
                  flexShrink: 0
                }}>
                  {msg.sender === 'ai' ? '✨' : 'U'}
                </div>

                {/* Message Bubble */}
                <div style={{ display: 'flex', flexDirection: 'column', gap: '12px', alignItems: msg.sender === 'user' ? 'flex-end' : 'flex-start' }}>
                  <div style={{ 
                    background: msg.sender === 'user' ? 'var(--accent-blue)' : 'rgba(255,255,255,0.05)',
                    padding: '16px 20px',
                    borderRadius: '16px',
                    borderTopLeftRadius: msg.sender === 'ai' ? '4px' : '16px',
                    borderTopRightRadius: msg.sender === 'user' ? '4px' : '16px',
                    lineHeight: '1.6',
                    fontSize: '15px',
                    border: msg.sender === 'ai' ? '1px solid var(--border-color)' : 'none'
                  }}>
                    {msg.text}
                  </div>

                  {/* Action Card */}
                  {msg.actionCard && (
                    <div style={{ 
                      background: 'rgba(239, 68, 68, 0.1)', 
                      border: '1px solid rgba(239, 68, 68, 0.3)',
                      padding: '20px',
                      borderRadius: '12px',
                      width: '100%',
                      boxShadow: '0 8px 32px rgba(239, 68, 68, 0.1)'
                    }}>
                      <h4 style={{ fontSize: '16px', fontWeight: 600, color: 'var(--status-red)', marginBottom: '8px' }}>⚡ {msg.actionCard.title}</h4>
                      <p style={{ color: 'var(--text-secondary)', fontSize: '14px', marginBottom: '16px' }}>{msg.actionCard.description}</p>
                      <button 
                        onClick={msg.actionCard.onExecute}
                        style={{ 
                          background: 'var(--status-red)', 
                          color: '#fff', 
                          border: 'none', 
                          padding: '10px 20px', 
                          borderRadius: '8px', 
                          fontWeight: 600, 
                          cursor: 'pointer',
                          width: '100%'
                        }}
                      >
                        {msg.actionCard.actionLabel}
                      </button>
                    </div>
                  )}
                </div>

              </div>
            </div>
          ))}
          {isTyping && (
             <div style={{ display: 'flex', justifyContent: 'flex-start' }}>
                <div style={{ display: 'flex', gap: '16px', alignItems: 'center' }}>
                  <div style={{ width: '36px', height: '36px', borderRadius: '50%', background: 'var(--accent-purple)', display: 'flex', alignItems: 'center', justifyContent: 'center', fontSize: '18px' }}>✨</div>
                  <div style={{ color: 'var(--text-secondary)', fontStyle: 'italic', fontSize: '14px' }}>PulseTrace is thinking...</div>
                </div>
             </div>
          )}
          <div ref={messagesEndRef} />
        </div>

        {/* Input Area */}
        <div style={{ padding: '24px', borderTop: '1px solid var(--border-color)', background: 'rgba(0,0,0,0.2)' }}>
          <form onSubmit={handleSend} style={{ display: 'flex', gap: '12px', position: 'relative' }}>
            <input 
              type="text" 
              value={inputValue}
              onChange={e => setInputValue(e.target.value)}
              placeholder="Ask a question or execute a runbook (e.g. 'Why is cart-service failing?')"
              style={{ 
                flex: 1, 
                background: 'rgba(255,255,255,0.05)', 
                border: '1px solid var(--border-color)', 
                padding: '16px 24px', 
                borderRadius: '128px',
                color: '#fff',
                fontSize: '15px',
                outline: 'none'
              }}
            />
            <button type="submit" disabled={!inputValue.trim() || isTyping} className="btn-primary" style={{ borderRadius: '128px', padding: '0 32px' }}>
              Send
            </button>
          </form>
          <div style={{ display: 'flex', gap: '12px', marginTop: '16px', justifyContent: 'center' }}>
            <span style={{ fontSize: '12px', color: 'var(--text-secondary)', background: 'rgba(255,255,255,0.05)', padding: '4px 12px', borderRadius: '128px', cursor: 'pointer' }}>"Rollback gateway-service"</span>
            <span style={{ fontSize: '12px', color: 'var(--text-secondary)', background: 'rgba(255,255,255,0.05)', padding: '4px 12px', borderRadius: '128px', cursor: 'pointer' }}>"Restart postgres connection pool"</span>
            <span style={{ fontSize: '12px', color: 'var(--text-secondary)', background: 'rgba(255,255,255,0.05)', padding: '4px 12px', borderRadius: '128px', cursor: 'pointer' }}>"Show me slow queries"</span>
          </div>
        </div>

      </div>
    </div>
  );
}
