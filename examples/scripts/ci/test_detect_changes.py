import pytest
from detect_changes import detect_affected_services

def test_detect_affected_services_basic():
    all_services = ["auth", "billing", "gateway"]
    changed_paths = ["services/auth/main.go", "README.md"]
    
    affected = detect_affected_services(all_services, changed_paths)
    assert affected == ["auth"]

def test_detect_affected_services_shared():
    all_services = ["auth", "billing"]
    changed_paths = ["shared/lib.go"]
    
    affected = detect_affected_services(all_services, changed_paths)
    assert affected == all_services

def test_detect_affected_services_none():
    all_services = ["auth", "billing"]
    changed_paths = ["docs/api.md"]
    
    affected = detect_affected_services(all_services, changed_paths)
    assert affected == []

def test_detect_affected_services_multiple():
    all_services = ["auth", "billing", "gateway"]
    changed_paths = ["services/auth/main.go", "services/gateway/proxy.go"]
    
    affected = detect_affected_services(all_services, changed_paths)
    assert sorted(affected) == ["auth", "gateway"]
