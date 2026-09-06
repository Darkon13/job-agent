CREATE TABLE application_campaigns (
    campaign_id TEXT PRIMARY KEY,
    job_tag TEXT NOT NULL,
    profiles BLOB NOT NULL,
    routes BLOB NOT NULL,
    target_successful INTEGER NOT NULL CHECK (target_successful > 0),
    max_in_flight INTEGER NOT NULL CHECK (max_in_flight > 0),
    route_index INTEGER NOT NULL CHECK (route_index >= 0),
    cursor TEXT NOT NULL,
    route_done INTEGER NOT NULL,
    status TEXT NOT NULL CHECK (status IN (
        'running', 'target_reached', 'exhausted', 'paused_budget',
        'paused_rate_limit', 'failed'
    )),
    stop_reason TEXT NOT NULL,
    correlation_id TEXT NOT NULL,
    revision INTEGER NOT NULL CHECK (revision > 0),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE application_campaign_items (
    campaign_id TEXT NOT NULL,
    application_id TEXT NOT NULL,
    route_index INTEGER NOT NULL CHECK (route_index >= 0),
    discovered_at INTEGER NOT NULL,
    PRIMARY KEY (campaign_id, application_id),
    FOREIGN KEY (campaign_id) REFERENCES application_campaigns(campaign_id),
    FOREIGN KEY (application_id) REFERENCES applications(id)
);

CREATE INDEX application_campaign_items_route_idx
    ON application_campaign_items(campaign_id, route_index, discovered_at);
