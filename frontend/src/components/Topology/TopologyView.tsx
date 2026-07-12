"use client";

import React, { useState, useEffect, useCallback } from 'react';
import ReactFlow, { 
  Node, 
  Edge, 
  Background, 
  Controls, 
  MiniMap,
  useNodesState,
  useEdgesState,
  Handle,
  Position
} from 'reactflow';
import 'reactflow/dist/style.css';

// Custom Node to match Apple Glassmorphism
const CustomNode = ({ data }: { data: any }) => {
  const getBorderColor = (state: string) => {
    switch (state) {
      case 'HEALTHY': return 'var(--status-green)';
      case 'DEGRADED': return 'var(--status-orange)';
      case 'UNHEALTHY': return 'var(--status-red)';
      default: return 'var(--border-color)';
    }
  };

  return (
    <div style={{
      background: 'var(--apple-glass-bg)',
      backdropFilter: 'var(--apple-glass-blur)',
      WebkitBackdropFilter: 'var(--apple-glass-blur)',
      border: `1px solid ${getBorderColor(data.state)}`,
      borderRadius: '12px',
      padding: '16px',
      minWidth: '150px',
      boxShadow: data.state === 'UNHEALTHY' ? '0 0 20px rgba(239, 68, 68, 0.4)' : '0 8px 32px rgba(0, 0, 0, 0.4)',
      color: 'var(--text-primary)',
      textAlign: 'center',
      transition: 'all 0.3s ease'
    }}>
      <Handle type="target" position={Position.Top} style={{ background: '#555' }} />
      <div style={{ fontWeight: 600, fontSize: '14px', marginBottom: '4px' }}>{data.label}</div>
      <div style={{ 
        fontSize: '10px', 
        color: getBorderColor(data.state),
        background: 'rgba(255,255,255,0.05)',
        padding: '2px 8px',
        borderRadius: '10px',
        display: 'inline-block'
      }}>
        {data.state}
      </div>
      <Handle type="source" position={Position.Bottom} style={{ background: '#555' }} />
    </div>
  );
};

const nodeTypes = {
  custom: CustomNode,
};

export function TopologyView() {
  const [nodes, setNodes, onNodesChange] = useNodesState([]);
  const [edges, setEdges, onEdgesChange] = useEdgesState([]);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    // Fetch topology data from the backend
    fetch('http://localhost:8084/api/v1/topology/graph')
      .then(res => res.json())
      .then(data => {
        if (data && data.nodes) {
          const rfNodes = data.nodes.map((n: any, idx: number) => ({
            id: n.id,
            type: 'custom',
            data: { label: n.id, state: n.state },
            position: { x: (idx % 3) * 250, y: Math.floor(idx / 3) * 150 }, // Auto-layout mock
          }));
          
          const rfEdges = data.edges ? data.edges.map((e: any, idx: number) => ({
            id: `e${idx}`,
            source: e.from,
            target: e.to,
            animated: true,
            style: { stroke: 'var(--accent-blue)', strokeWidth: 2 }
          })) : [];

          setNodes(rfNodes);
          setEdges(rfEdges);
        }
        setLoading(false);
      })
      .catch(err => {
        console.error("Failed to fetch topology:", err);
        setLoading(false);
      });
  }, []);

  return (
    <div style={{ display: 'flex', flexDirection: 'column', height: 'calc(100vh - 120px)' }}>
      
      {/* Toolbar */}
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '24px' }}>
        <div>
          <h2 style={{ fontSize: '24px', fontWeight: 600, marginBottom: '8px' }}>Interactive Service Topology</h2>
          <p style={{ color: 'var(--text-secondary)' }}>Live dependency map derived from distributed traces.</p>
        </div>
        <div style={{ display: 'flex', gap: '12px' }}>
          <button className="btn-primary" style={{ padding: '8px 16px', display: 'flex', alignItems: 'center', gap: '8px' }}>
            <span>⚡️</span> AI Root Cause
          </button>
        </div>
      </div>

      {/* React Flow Graph Container */}
      <div className="glass-panel" style={{ flex: 1, overflow: 'hidden', position: 'relative' }}>
        {loading ? (
          <div style={{ display: 'flex', height: '100%', justifyContent: 'center', alignItems: 'center', color: 'var(--text-secondary)' }}>
            Discovering topology...
          </div>
        ) : (
          <ReactFlow
            nodes={nodes}
            edges={edges}
            onNodesChange={onNodesChange}
            onEdgesChange={onEdgesChange}
            nodeTypes={nodeTypes}
            fitView
            style={{ background: 'transparent' }}
          >
            <Background color="rgba(255,255,255,0.1)" gap={16} />
            <Controls style={{ button: { background: 'var(--apple-glass-bg)', color: 'white', border: '1px solid var(--border-color)' } }} />
            <MiniMap 
              nodeColor={(n) => {
                if (n.data?.state === 'UNHEALTHY') return 'var(--status-red)';
                if (n.data?.state === 'DEGRADED') return 'var(--status-orange)';
                return 'var(--status-green)';
              }}
              maskColor="rgba(0,0,0,0.5)"
              style={{ background: 'var(--apple-glass-bg)', border: '1px solid var(--border-color)' }}
            />
          </ReactFlow>
        )}
      </div>

    </div>
  );
}
