CREATE TABLE IF NOT EXISTS users (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    username VARCHAR(255) UNIQUE NOT NULL,
    password_hash VARCHAR(255) NOT NULL,
    role VARCHAR(50) NOT NULL DEFAULT 'viewer',
    tenant_id VARCHAR(50) NOT NULL DEFAULT 'default',
    tier VARCHAR(50) NOT NULL DEFAULT 'standard',
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

-- Insert default admin user (password: 'admin')
-- Note: In a real production system, do not hardcode the hash like this, or force a password change on first login.
-- This bcrypt hash is for the word 'admin'.
INSERT INTO users (username, password_hash, role, tenant_id, tier)
VALUES ('admin', '$2a$10$U/LidIHsI1cfo77RK4nWq.65os/hGFf.xL053HK3KNGEKybKMtPFi', 'admin', 'default', 'standard')
ON CONFLICT (username) DO NOTHING;
