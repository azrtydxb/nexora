-- +goose Up
-- System users (source system) own the API tokens of automation such as the Kubernetes operator's
-- bootstrap token; they have no password and never count as a human user or admin.
alter table users drop constraint users_source_check;
alter table users add constraint users_source_check check (source in ('local', 'oidc', 'system'));

-- +goose Down
delete from users where source = 'system';
alter table users drop constraint users_source_check;
alter table users add constraint users_source_check check (source in ('local', 'oidc'));
